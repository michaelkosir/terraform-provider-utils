package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/action"
	actionschema "github.com/hashicorp/terraform-plugin-framework/action/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
)

const ssmPollInterval = 5 * time.Second

var _ action.Action = &ssmSendCommandAction{}

// NewSSMSendCommandAction is the constructor registered in the provider's Actions() method.
func NewSSMSendCommandAction() action.Action {
	return &ssmSendCommandAction{}
}

// ssmSendCommandAction sends an AWS SSM Run Command to deploy the leaderboard application.
type ssmSendCommandAction struct{}

type ssmSendCommandActionModel struct {
	InstanceID       types.String `tfsdk:"instance_id"`
	Commands         types.List   `tfsdk:"commands"`
	WorkingDirectory types.String `tfsdk:"working_directory"`
	Region           types.String `tfsdk:"region"`
	WaitSeconds      types.Int64  `tfsdk:"wait"`
	Timeout          types.Int64  `tfsdk:"timeout"`
}

func (a *ssmSendCommandAction) Metadata(_ context.Context, _ action.MetadataRequest, resp *action.MetadataResponse) {
	resp.TypeName = "utils_ssm_send_command"
}

func (a *ssmSendCommandAction) Schema(_ context.Context, _ action.SchemaRequest, resp *action.SchemaResponse) {
	resp.Schema = actionschema.Schema{
		MarkdownDescription: "Sends an AWS SSM Run Command to the target instance specified by `instance_id`, " +
			"executing the shell commands listed in `commands`.",
		Attributes: map[string]actionschema.Attribute{
			"instance_id": actionschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "EC2 instance ID (e.g. `i-0abc123def456`). Passed directly as `--instance-ids` to the SSM SendCommand API.",
			},
			"commands": actionschema.ListAttribute{
				Required:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Ordered list of shell commands to run on the target instance.",
				Validators: []validator.List{
					listvalidator.SizeAtLeast(1),
				},
			},
			"working_directory": actionschema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Working directory on the target instance from which all commands are executed. " +
					"Passed as the SSM `workingDirectory` parameter.",
			},
			"region": actionschema.StringAttribute{
				Optional: true,
				MarkdownDescription: "AWS region override (e.g. `us-east-1`). " +
					"Defaults to the region resolved by the AWS SDK default credential chain.",
			},
			"wait": actionschema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "Maximum seconds to wait for the instance to register with SSM before dispatching the command. " +
					"The action polls `DescribeInstanceInformation` with exponential backoff (2s→30s) until the instance appears as `Online`. " +
					"If omitted, the command is dispatched immediately without any registration check.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"timeout": actionschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum seconds to wait for the command to complete after it has been dispatched. Defaults to `120` if not set.",
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
		},
	}
}

func (a *ssmSendCommandAction) Invoke(ctx context.Context, req action.InvokeRequest, resp *action.InvokeResponse) {
	var data ssmSendCommandActionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Build AWS config - optional region override.
	var cfgOpts []func(*awsconfig.LoadOptions) error
	if !data.Region.IsNull() && data.Region.ValueString() != "" {
		cfgOpts = append(cfgOpts, awsconfig.WithRegion(data.Region.ValueString()))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, cfgOpts...)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to load AWS configuration",
			fmt.Sprintf("could not load AWS credentials: %v", err),
		)
		return
	}

	client := ssm.NewFromConfig(awsCfg)
	instanceID := data.InstanceID.ValueString()

	// Poll with exponential backoff until the instance is registered and online
	// with SSM before dispatching the command.
	if !data.WaitSeconds.IsNull() && !data.WaitSeconds.IsUnknown() {
		regTimeout := time.Duration(data.WaitSeconds.ValueInt64()) * time.Second
		resp.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Waiting up to %s for instance %q to register with SSM...", regTimeout, instanceID),
		})

		regCtx, regCancel := context.WithTimeout(ctx, regTimeout)
		defer regCancel()

		waiter := &retry.StateChangeConf{
			Pending:      []string{"pending"},
			Target:       []string{"online"},
			Delay:        2 * time.Second,
			MinTimeout:   2 * time.Second,
			PollInterval: 0, // 0 enables exponential backoff
			Timeout:      regTimeout,
			Refresh: func() (any, string, error) {
				out, err := client.DescribeInstanceInformation(regCtx, &ssm.DescribeInstanceInformationInput{
					Filters: []ssmtypes.InstanceInformationStringFilter{
						{Key: aws.String("InstanceIds"), Values: []string{instanceID}},
					},
				})
				if err != nil {
					return nil, "pending", nil //nolint:nilerr // transient; keep polling
				}
				for _, info := range out.InstanceInformationList {
					if aws.ToString(info.InstanceId) == instanceID &&
						info.PingStatus == ssmtypes.PingStatusOnline {
						return info, "online", nil
					}
				}
				return nil, "pending", nil
			},
		}

		if _, err := waiter.WaitForStateContext(regCtx); err != nil {
			resp.Diagnostics.AddError(
				"Instance did not register with SSM in time",
				fmt.Sprintf("Instance %q did not appear as Online in SSM within %s: %v", instanceID, regTimeout, err),
			)
			return
		}

		resp.SendProgress(action.InvokeProgressEvent{
			Message: fmt.Sprintf("Instance %q is registered with SSM. Dispatching command...", instanceID),
		})
	}

	// Apply command completion timeout.
	timeoutSec := int64(120)
	if !data.Timeout.IsNull() && !data.Timeout.IsUnknown() {
		timeoutSec = data.Timeout.ValueInt64()
	}

	// Convert types.List -> []string.
	var commands []string
	resp.Diagnostics.Append(data.Commands.ElementsAs(ctx, &commands, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Sending SSM Run Command to target %q...", instanceID),
	})

	params := map[string][]string{
		"commands": commands,
	}
	if !data.WorkingDirectory.IsNull() && !data.WorkingDirectory.IsUnknown() {
		params["workingDirectory"] = []string{data.WorkingDirectory.ValueString()}
	}

	sendOut, err := client.SendCommand(ctx, &ssm.SendCommandInput{
		DocumentName: aws.String("AWS-RunShellScript"),
		InstanceIds:  []string{instanceID},
		Parameters:   params,
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"SSM SendCommand failed",
			fmt.Sprintf("could not send command to %q: %v", instanceID, err),
		)
		return
	}

	if sendOut.Command == nil {
		resp.Diagnostics.AddError(
			"SSM SendCommand returned no command",
			"the SSM API returned a successful response but no command object.",
		)
		return
	}
	commandID := aws.ToString(sendOut.Command.CommandId)
	resp.SendProgress(action.InvokeProgressEvent{
		Message: fmt.Sprintf("Command %q dispatched. Polling for completion (up to %ds)...", commandID, timeoutSec),
	})

	// Poll until the command finishes or the deadline is reached.
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	ticker := time.NewTicker(ssmPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				resp.Diagnostics.AddError(
					"SSM command timed out",
					fmt.Sprintf("Command %q did not complete within %d seconds.", commandID, timeoutSec),
				)
			} else {
				resp.Diagnostics.AddError(
					"Context cancelled",
					"the action context was cancelled before the command completed.",
				)
			}
			return
		case <-ticker.C:
		}

		listOut, err := client.ListCommandInvocations(waitCtx, &ssm.ListCommandInvocationsInput{
			CommandId: aws.String(commandID),
			Details:   true,
		})
		if err != nil {
			resp.Diagnostics.AddError(
				"SSM ListCommandInvocations failed",
				fmt.Sprintf("could not poll command %q: %v", commandID, err),
			)
			return
		}

		if len(listOut.CommandInvocations) > 0 {
			inv := listOut.CommandInvocations[0]
			switch inv.Status {
			case ssmtypes.CommandInvocationStatusSuccess:
				resp.SendProgress(action.InvokeProgressEvent{
					Message: fmt.Sprintf("Command %q completed successfully on %s.", commandID, instanceID),
				})
				return
			case ssmtypes.CommandInvocationStatusFailed,
				ssmtypes.CommandInvocationStatusCancelled,
				ssmtypes.CommandInvocationStatusTimedOut:
				resp.Diagnostics.AddError(
					"SSM command did not succeed",
					fmt.Sprintf("Command %q finished with status %q on instance %s.", commandID, inv.Status, instanceID),
				)
				return
			default:
				resp.SendProgress(action.InvokeProgressEvent{
					Message: fmt.Sprintf("Command %q status: %s. Waiting...", commandID, inv.Status),
				})
			}
		}
	}
}
