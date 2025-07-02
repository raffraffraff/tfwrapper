# tfwrapper

`tfwrapper` is a Go CLI tool that generates a Terraform wrapper module for any remote Terraform module. It creates a new module that accepts a single JSON-encoded input variable and returns all outputs as a single object, making it easier to integrate with automation and configuration management systems.

## Features
- Wraps any remote Terraform module (e.g., from the Terraform Registry or GitHub)
- Supports submodule paths (e.g., `terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks`)
- Accepts all module inputs as a single `config` variable (JSON-encoded)
- Returns all module outputs as a single output object
- Optionally supports iteration over a map of resources (`--iterable`)
- Automatically formats generated `.tf` files using HCL formatting
- No external dependencies on Terraform/OpenTofu CLI tools

## Requirements
- Go 1.18+
- Git (for cloning remote repositories)

## Usage

```sh
tfwrapper -source <MODULE_SOURCE> [-version <MODULE_VERSION>] [-name <WRAPPER_NAME>] [-iterable]
```

- `-source` (required): The source of the Terraform module (e.g., `github.com/org/module`)
- `-version` (optional): The module version to use (default: latest)
- `-name` (optional): The name for the generated wrapper module directory (defaults to the module name)
- `-iterable` (optional): If set, the wrapper will use `for_each` to iterate over a map of configs

### Example
Wrap version 5.1.0 of the terraform-aws-modules VPC module, in subdirectory `terraform-aws-vpc`:
```sh
tfwrapper -source github.com/terraform-aws-modules/terraform-aws-vpc -version 5.1.0
```

Wrap the latest version of the same module, in subdirectory `vpc`:
```sh
tfwrapper -source github.com/terraform-aws-modules/terraform-aws-vpc -name vpc
```

## Output
- `locals.tf`: Decodes the JSON `config` variable
- `variables.tf`: Declares the `config` variable
- `main.tf`: Instantiates the wrapped module, passing all variables from `config`
- `outputs.tf`: Returns all outputs as a single object

## License
MIT
