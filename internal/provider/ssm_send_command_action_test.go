package provider_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/michaelkosir/terraform-provider-utils/internal/provider"
)

// testAccProtoV6ProviderFactories is used by acceptance tests.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"utils": providerserver.NewProtocol6WithError(provider.New("test")()),
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_INSTANCE_ID") == "" {
		t.Fatal("TEST_INSTANCE_ID must be set for acceptance tests")
	}
	if os.Getenv("TEST_AWS_REGION") == "" {
		t.Fatal("TEST_AWS_REGION must be set for acceptance tests")
	}
}

func TestAccSSMSendCommandAction_basic(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Set TF_ACC to run acceptance tests")
	}
	instanceID := os.Getenv("TEST_INSTANCE_ID")
	region := os.Getenv("TEST_AWS_REGION")

	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_14_0),
		},
		Steps: []resource.TestStep{
			{
				Config: testAccSSMSendCommandActionConfig(instanceID, region),
			},
		},
	})
}

func testAccSSMSendCommandActionConfig(instanceID, region string) string {
	return fmt.Sprintf(`
provider "utils" {}

action "utils_ssm_send_command" "deploy" {
  config {
    region            = %[1]q
    instance_id       = %[2]q
    working_directory = "/opt/leaderboard"

    commands = [
      "sudo cloud-init status --wait",
      "touch test",
    ]
  }
}

resource "terraform_data" "trigger" {
  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.utils_ssm_send_command.deploy]
    }
  }
}
`, region, instanceID)
}
