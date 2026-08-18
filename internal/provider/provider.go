package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/action"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

var (
	_ provider.Provider            = &utilsProvider{}
	_ provider.ProviderWithActions = &utilsProvider{}
)

// New returns the provider factory consumed by main.go.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &utilsProvider{version: version}
	}
}

type utilsProvider struct {
	version string
}

func (p *utilsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "utils"
	resp.Version = p.version
}

func (p *utilsProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The Utils provider exposes actions for imperative operations.",
	}
}

func (p *utilsProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {
}

// Resources returns no managed resources - this provider is actions-only.
func (p *utilsProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

// DataSources returns no data sources - this provider is actions-only.
func (p *utilsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

// Actions returns all provider actions. Add new action constructors here.
func (p *utilsProvider) Actions(_ context.Context) []func() action.Action {
	return []func() action.Action{
		NewSSMSendCommandAction,
	}
}
