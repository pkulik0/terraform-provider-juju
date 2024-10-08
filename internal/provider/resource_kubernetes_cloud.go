// Copyright 2024 Canonical Ltd.
// Licensed under the Apache License, Version 2.0, see LICENCE file for details.

package provider

import (
	"context"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/juju/terraform-provider-juju/internal/juju"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &kubernetesCloudResource{}
var _ resource.ResourceWithConfigure = &kubernetesCloudResource{}
var _ resource.ResourceWithImportState = &kubernetesCloudResource{}

func NewKubernetesCloudResource() resource.Resource {
	return &kubernetesCloudResource{}
}

type kubernetesCloudResource struct {
	*juju.Client

	// subCtx is the context created with the new tflog subsystem for applications.
	context.Context
}

func (o *kubernetesCloudResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
}

func (o *kubernetesCloudResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (o *kubernetesCloudResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cloud"
}

func (o *kubernetesCloudResource) Schema(_ context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A resource that represent a Juju Cloud for existing controller.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "The name of the cloud. Changing this value will cause the cloud to be destroyed and recreated by terraform.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kubeconfig": schema.StringAttribute{
				Description: "The kubeconfig file path for the cloud.",
				Optional:    true,
				Sensitive:   true,
			},
			"parentcloudname": schema.StringAttribute{
				Description: "The parent cloud name in case adding k8s cluster from existed cloud. Changing this value will cause the cloud to be destroyed and recreated by terraform.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"parentcloudregion": schema.StringAttribute{
				Description: "The parent cloud region name in case adding k8s cluster from existed cloud. Changing this value will cause the cloud to be destroyed and recreated by terraform.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Create adds a new kubernetes cloud to controllers used now by Terraform provider.
func (o *kubernetesCloudResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
}

// Read reads the current state of the kubernetes cloud.
func (o *kubernetesCloudResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

// Update updates the kubernetes cloud on the controller used by Terraform provider.
func (o *kubernetesCloudResource) Update(context.Context, resource.UpdateRequest, *resource.UpdateResponse) {
}

// Delete removes the kubernetes cloud from the controller used by Terraform provider.
func (o *kubernetesCloudResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}
