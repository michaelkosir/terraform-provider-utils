terraform {
  required_providers {
    utils = {
      source = "michaelkosir/utils"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

provider "utils" {}

resource "aws_instance" "app" {
  ami           = "ami-0c02fb55956c7d316" # Amazon Linux 2 (us-east-1)
  instance_type = "t3.micro"

  tags = {
    Name = "app-server"
  }
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
