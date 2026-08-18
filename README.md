# terraform-provider-utils

A Terraform provider that exposes imperative operations as [actions](https://developer.hashicorp.com/terraform/plugin/framework/actions) - no managed resources or data sources.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.12
- [Go](https://golang.org/doc/install) >= 1.22 (to build from source)

## Usage

```hcl
terraform {
  required_providers {
    utils = {
      source = "michaelkosir/utils"
    }
  }
}

provider "utils" {}
```

The provider requires no configuration. AWS credentials are resolved via the standard AWS SDK credential chain (environment variables, shared credentials file, IAM instance profile, etc.).

## Actions

### `utils_ssm_send_command`

Sends an AWS SSM Run Command to a target EC2 instance and waits for it to complete.

#### Schema

| Attribute | Type | Required | Description |
|---|---|---|---|
| `instance_id` | String | Yes | EC2 instance ID (e.g. `i-0abc123def456`) |
| `commands` | List of String | Yes | Ordered list of shell commands to run |
| `working_directory` | String | No | Working directory on the target instance |
| `region` | String | No | AWS region override. Defaults to the SDK credential chain region |
| `wait_seconds` | Number | No | Max seconds to wait for completion. Defaults to `120` |

#### Example

```hcl
resource "aws_instance" "app" {
  ami           = "ami-0c02fb55956c7d316"
  instance_type = "t3.micro"
}

action "utils_ssm_send_command" "deploy" {
  config {
    instance_id       = aws_instance.app.id
    region            = "us-east-1"
    working_directory = "/home/ec2-user"
    wait_seconds      = 60

    commands = [
      "echo 'Starting deployment'",
      "cd /home/ec2-user && ./deploy.sh",
      "echo 'Deployment complete'",
    ]
  }
}

resource "terraform_data" "trigger" {
  triggers_replace = [aws_instance.app.id]

  lifecycle {
    action_trigger {
      events  = [after_create]
      actions = [action.utils_ssm_send_command.deploy]
    }
  }
}
```

## Development

### Build

```shell
go build ./...
```

### Test

```shell
go test ./...
```

Acceptance tests require AWS credentials and a reachable EC2 instance with SSM Agent installed. Set `TF_ACC=1` to run them.

### Generate docs

```shell
make generate
```
