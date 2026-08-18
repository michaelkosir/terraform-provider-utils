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
	WaitSeconds      types.Int64  `tfsdk:"wait_seconds"`
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
			"wait_seconds": actionschema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Maximum seconds to wait for the command to complete. Defaults to `120` if not set.",
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

	// Apply default wait.
	waitSec := int64(120)
	if !data.WaitSeconds.IsNull() && !data.WaitSeconds.IsUnknown() {
		waitSec = data.WaitSeconds.ValueInt64()
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
		Message: fmt.Sprintf("Command %q dispatched. Polling for completion (up to %ds)...", commandID, waitSec),
	})

	// Poll until the command finishes or the deadline is reached.
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(waitSec)*time.Second)
	defer cancel()

	ticker := time.NewTicker(ssmPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				resp.Diagnostics.AddError(
					"SSM command timed out",
					fmt.Sprintf("Command %q did not complete within %d seconds.", commandID, waitSec),
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
