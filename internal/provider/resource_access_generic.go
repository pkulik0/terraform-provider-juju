// Copyright 2024 Canonical Ltd.
// Licensed under the Apache License, Version 2.0, see LICENCE file for details.

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	jimmnames "github.com/canonical/jimm-go-sdk/v3/names"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/juju/names/v5"

	"github.com/juju/terraform-provider-juju/internal/juju"
)

var (
	basicEmailValidationRe = regexp.MustCompile(".+@.+")
	avoidAtSymbolRe        = regexp.MustCompile("^[^@]*$")
)

// Getter is used to get details from a plan or state object.
// Implemented by Terraform's [State] and [Plan] types.
type Getter interface {
	Get(ctx context.Context, target interface{}) diag.Diagnostics
}

// Setter is used to set details on a state object.
// Implemented by Terraform's [State] type.
type Setter interface {
	Set(ctx context.Context, target interface{}) diag.Diagnostics
}

// resourcer defines how the [genericJAASAccessResource] can query/save for information
// on the target object.
type resourcer interface {
	Info(ctx context.Context, getter Getter, diag *diag.Diagnostics) (genericJAASAccessModel, names.Tag)
	Save(ctx context.Context, setter Setter, info genericJAASAccessModel, tag names.Tag) diag.Diagnostics
	ImportHint() string
}

// genericJAASAccessResource is a generic resource that can be used for creating access rules with JAAS.
// Other types should embed this struct and implement their own metadata and schema methods. The schema
// should build on top of [PartialAccessSchema].
// The embedded struct requires a targetInfo interface to enable fetching the target object in the relation.
type genericJAASAccessResource struct {
	client          *juju.Client
	targetResource  resourcer
	resourceLogName string

	// subCtx is the context created with the new tflog subsystem for applications.
	subCtx context.Context
}

// genericJAASAccessModel represents a partial generic object for access management.
// This struct should be embedded into a struct that contains a field for a target object (normally a name or UUID).
// Note that service accounts are treated as users but kept as a separate field for improved validation.
type genericJAASAccessModel struct {
	Users           types.Set    `tfsdk:"users"`
	ServiceAccounts types.Set    `tfsdk:"service_accounts"`
	Groups          types.Set    `tfsdk:"groups"`
	Access          types.String `tfsdk:"access"`

	// ID required for imports
	ID types.String `tfsdk:"id"`
}

// ConfigValidators sets validators for the resource.
func (r *genericJAASAccessResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		NewRequiresJAASValidator(r.client),
		resourcevalidator.AtLeastOneOf(
			path.MatchRoot("users"),
			path.MatchRoot("groups"),
			path.MatchRoot("service_accounts"),
		),
	}
}

// partialAccessSchema returns a map of schema attributes for a JAAS access resource.
// Access resources should use this schema and add any additional attributes e.g. name or uuid.
func (r *genericJAASAccessResource) partialAccessSchema() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"access": schema.StringAttribute{
			Description: "Level of access to grant. Changing this value will replace the Terraform resource.",
			Required:    true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.RequiresReplace(),
			},
		},
		"users": schema.SetAttribute{
			Description: "List of users to grant access.",
			Optional:    true,
			ElementType: types.StringType,
			Validators: []validator.Set{
				setvalidator.ValueStringsAre(ValidatorMatchString(names.IsValidUser, "email must be a valid Juju username")),
				setvalidator.ValueStringsAre(stringvalidator.RegexMatches(basicEmailValidationRe, "email must contain an @ symbol")),
			},
		},
		"groups": schema.SetAttribute{
			Description: "List of groups to grant access.",
			Optional:    true,
			ElementType: types.StringType,
			Validators: []validator.Set{
				setvalidator.ValueStringsAre(ValidatorMatchString(jimmnames.IsValidGroupId, "group ID must be valid")),
			},
		},
		"service_accounts": schema.SetAttribute{
			Description: "List of service accounts to grant access.",
			Optional:    true,
			ElementType: types.StringType,
			// service accounts are treated as users but defined separately
			// for different validation and logic in the provider.
			Validators: []validator.Set{
				setvalidator.ValueStringsAre(ValidatorMatchString(
					func(s string) bool {
						// Use EnsureValidServiceAccountId instead of IsValidServiceAccountId
						// because we avoid requiring the user to add @serviceaccount for service accounts
						// and opt to add that in the provide code. EnsureValidServiceAccountId adds the
						// @serviceaccount domain before verifying the string is a valid service account ID.
						_, err := jimmnames.EnsureValidServiceAccountId(s)
						return err == nil
					}, "service account ID must be a valid Juju username")),
				setvalidator.ValueStringsAre(stringvalidator.RegexMatches(avoidAtSymbolRe, "service account should not contain an @ symbol")),
			},
		},
		// ID required for imports
		"id": schema.StringAttribute{
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
	}
}

// Configure enables provider-level data or clients to be set in the
// provider-defined DataSource type. It is separately executed for each
// ReadDataSource RPC.
func (resource *genericJAASAccessResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*juju.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *juju.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	resource.client = client
	// Create the local logging subsystem here, using the TF context when creating it.
	resource.subCtx = tflog.NewSubsystem(ctx, resource.resourceLogName)
}

// Create defines how tuples for access control will be created.
func (resource *genericJAASAccessResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Check first if the client is configured
	if resource.client == nil {
		addClientNotConfiguredError(&resp.Diagnostics, resource.resourceLogName, "create")
		return
	}

	// Read Terraform configuration from the request into the model
	plan, targetTag := resource.info(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create tuples to create from the plan
	tuples := modelToTuples(ctx, targetTag, plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// Make a call to create relations
	err := resource.client.Jaas.AddRelations(tuples)
	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to create access relationships for %s, got error: %s", targetTag.String(), err))
		return
	}
	plan.ID = types.StringValue(newJaasAccessID(targetTag, plan.Access.ValueString()))
	// Set the plan onto the Terraform state
	resp.Diagnostics.Append(resource.targetResource.Save(ctx, &resp.State, plan, targetTag)...)
}

// Read defines how tuples for access control will be read.
func (resource *genericJAASAccessResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Check first if the client is configured
	if resource.client == nil {
		addClientNotConfiguredError(&resp.Diagnostics, resource.resourceLogName, "read")
		return
	}

	// Read Terraform configuration from the request into the resource model
	// Ignore the target tag as it will come from the ID
	state, _ := resource.info(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Retrieve information necessary for reads from the ID to handle imports
	targetTag, access := retrieveJaasAccessFromID(state.ID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Perform read request for relations
	readTuple := juju.JaasTuple{
		Target:   targetTag.String(),
		Relation: access,
	}
	tuples, err := resource.client.Jaas.ReadRelations(ctx, &readTuple)
	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read access rules for %s, got error: %s", targetTag.String(), err))
		return
	}

	// Transform the tuples into an access model
	newModel := tuplesToModel(ctx, tuples, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Users = newModel.Users
	state.Groups = newModel.Groups
	state.ServiceAccounts = newModel.ServiceAccounts
	state.Access = basetypes.NewStringValue(access)
	resp.Diagnostics.Append(resource.targetResource.Save(ctx, &resp.State, state, targetTag)...)
}

// Update defines how tuples for access control will be updated.
func (resource *genericJAASAccessResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Check first if the client is configured
	if resource.client == nil {
		addClientNotConfiguredError(&resp.Diagnostics, resource.resourceLogName, "update")
		return
	}

	// Note: We only need to read the targetID from either the plan or the state.
	// If it changed, the resource should be replaced rather than updated.
	// The same also applies to the access level.
	// For this reason we don't need to update the ID as a new ID implies a different resource.

	// Read Terraform configuration from the state
	state, targetTag := resource.info(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read Terraform configuration from the plan
	plan, _ := resource.info(ctx, req.Plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Get a diff of the plan vs. state to know what relations to add/remove
	modelAdd, modelRemove := diffModels(plan, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create a list of tuples to add and tuples to remove
	addTuples := modelToTuples(ctx, targetTag, modelAdd, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	removeTuples := modelToTuples(ctx, targetTag, modelRemove, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Add new relations
	if len(addTuples) > 0 {
		err := resource.client.Jaas.AddRelations(addTuples)
		if err != nil {
			resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to add access rules for %s, got error: %s", targetTag.String(), err))
			return
		}
	}

	// TODO: Consider updating the state here to reflect the newly added tuples before removing tuples in case the next removal fails.
	// Would require an intermediate state.

	// Delete removed relations
	if len(removeTuples) > 0 {
		err := resource.client.Jaas.DeleteRelations(removeTuples)
		if err != nil {
			resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to remove access rules for %s, got error: %s", targetTag.String(), err))
			return
		}
	}

	// Set the desired plan onto the Terraform state after all updates have taken place.
	resp.Diagnostics.Append(resource.save(ctx, &resp.State, plan, targetTag)...)
}

func diffModels(plan, state genericJAASAccessModel, diag *diag.Diagnostics) (toAdd, toRemove genericJAASAccessModel) {
	newUsers := diffSet(plan.Users, state.Users, diag)
	newGroups := diffSet(plan.Groups, state.Groups, diag)
	newServiceAccounts := diffSet(plan.ServiceAccounts, state.ServiceAccounts, diag)
	toAdd.Users = newUsers
	toAdd.Groups = newGroups
	toAdd.ServiceAccounts = newServiceAccounts
	toAdd.Access = plan.Access

	removedUsers := diffSet(state.Users, plan.Users, diag)
	removedGroups := diffSet(state.Groups, plan.Groups, diag)
	removedServiceAccounts := diffSet(state.ServiceAccounts, plan.ServiceAccounts, diag)
	toRemove.Users = removedUsers
	toRemove.Groups = removedGroups
	toRemove.ServiceAccounts = removedServiceAccounts
	toRemove.Access = plan.Access

	return
}

// diffSet returns the elements in the target set that are not present in the current set.
func diffSet(current, target basetypes.SetValue, diag *diag.Diagnostics) basetypes.SetValue {
	var diff []attr.Value
	for _, source := range current.Elements() {
		found := false
		for _, target := range target.Elements() {
			if source.Equal(target) {
				found = true
			}
		}
		if !found {
			diff = append(diff, source)
		}
	}
	newSet, diags := basetypes.NewSetValue(current.ElementType(context.Background()), diff)
	diag.Append(diags...)
	return newSet
}

// Delete defines how tuples for access control will be deleted.
func (resource *genericJAASAccessResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Check first if the client is configured
	if resource.client == nil {
		addClientNotConfiguredError(&resp.Diagnostics, "access model", "delete")
		return
	}

	// Read Terraform configuration from the state
	state, targetTag := resource.info(ctx, req.State, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Create the tuples to delete
	tuples := modelToTuples(ctx, targetTag, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// Delete the tuples
	err := resource.client.Jaas.DeleteRelations(tuples)
	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to delete access rules for %s, got error: %s", targetTag.String(), err))
		return
	}
}

// modelToTuples return a list of tuples based on the access model provided.
func modelToTuples(ctx context.Context, targetTag names.Tag, model genericJAASAccessModel, diag *diag.Diagnostics) []juju.JaasTuple {
	var users []string
	var groups []string
	var serviceAccounts []string
	diag.Append(model.Users.ElementsAs(ctx, &users, false)...)
	diag.Append(model.Groups.ElementsAs(ctx, &groups, false)...)
	diag.Append(model.ServiceAccounts.ElementsAs(ctx, &serviceAccounts, false)...)
	if diag.HasError() {
		return []juju.JaasTuple{}
	}
	baseTuple := juju.JaasTuple{
		Target:   targetTag.String(),
		Relation: model.Access.ValueString(),
	}
	var tuples []juju.JaasTuple
	userNameToTagf := func(s string) string { return names.NewUserTag(s).String() }
	groupIDToTagf := func(s string) string { return jimmnames.NewGroupTag(s).String() }
	// Note that service accounts are treated as users but with an @serviceaccount domain.
	// We add the @serviceaccount domain by calling `EnsureValidServiceAccountId` so that the user writing the plan doesn't have to.
	// We can ignore the error below because the inputs have already gone through validation.
	serviceAccIDToTagf := func(s string) string {
		r, _ := jimmnames.EnsureValidServiceAccountId(s)
		return names.NewUserTag(r).String()
	}
	tuples = append(tuples, assignTupleObject(baseTuple, users, userNameToTagf)...)
	tuples = append(tuples, assignTupleObject(baseTuple, groups, groupIDToTagf)...)
	tuples = append(tuples, assignTupleObject(baseTuple, serviceAccounts, serviceAccIDToTagf)...)
	return tuples
}

// tuplesToModel does the reverse of planToTuples converting a slice of tuples to an access model.
func tuplesToModel(ctx context.Context, tuples []juju.JaasTuple, diag *diag.Diagnostics) genericJAASAccessModel {
	var users []string
	var groups []string
	var serviceAccounts []string
	for _, tuple := range tuples {
		tag, err := jimmnames.ParseTag(tuple.Object)
		if err != nil {
			diag.AddError("failed to parse relation tag", fmt.Sprintf("error parsing %s:%s", tuple.Object, err.Error()))
			continue
		}
		switch tag.Kind() {
		case names.UserTagKind:
			userTag := tag.(names.UserTag)
			if jimmnames.IsValidServiceAccountId(userTag.Id()) {
				// Remove the domain so it matches the plan.
				svcAccount := userTag.Id()
				domainStart := strings.IndexRune(userTag.Id(), '@')
				if domainStart != -1 {
					svcAccount = svcAccount[:domainStart]
				}
				serviceAccounts = append(serviceAccounts, svcAccount)
			} else {
				users = append(users, userTag.Id())
			}
		case jimmnames.GroupTagKind:
			groups = append(groups, tag.Id())
		}
	}
	userSet, errDiag := basetypes.NewSetValueFrom(ctx, types.StringType, users)
	diag.Append(errDiag...)
	groupSet, errDiag := basetypes.NewSetValueFrom(ctx, types.StringType, groups)
	diag.Append(errDiag...)
	serviceAccountSet, errDiag := basetypes.NewSetValueFrom(ctx, types.StringType, serviceAccounts)
	diag.Append(errDiag...)
	var model genericJAASAccessModel
	model.Users = userSet
	model.Groups = groupSet
	model.ServiceAccounts = serviceAccountSet
	return model
}

func assignTupleObject(baseTuple juju.JaasTuple, items []string, idToTag func(string) string) []juju.JaasTuple {
	tuples := make([]juju.JaasTuple, 0, len(items))
	for _, item := range items {
		t := baseTuple
		t.Object = idToTag(item)
		tuples = append(tuples, t)
	}
	return tuples
}

func (a *genericJAASAccessResource) info(ctx context.Context, getter Getter, diags *diag.Diagnostics) (genericJAASAccessModel, names.Tag) {
	return a.targetResource.Info(ctx, getter, diags)
}

func (a *genericJAASAccessResource) save(ctx context.Context, setter Setter, info genericJAASAccessModel, tag names.Tag) diag.Diagnostics {
	return a.targetResource.Save(ctx, setter, info, tag)
}

func newJaasAccessID(targetTag names.Tag, accessStr string) string {
	return fmt.Sprintf("%s:%s", targetTag.String(), accessStr)
}

func retrieveJaasAccessFromID(ID types.String, diag *diag.Diagnostics) (resourceTag names.Tag, access string) {
	resID := strings.Split(ID.ValueString(), ":")
	if len(resID) != 2 {
		diag.AddError("Malformed ID", fmt.Sprintf("Access ID %q is malformed, "+
			"please use the format '<resourceTag>:<access>:'", resID))
		return nil, ""
	}
	tag, err := jimmnames.ParseTag(resID[0])
	if err != nil {
		diag.AddError("ID Error", fmt.Sprintf("Tag %s from ID is not valid: %s", tag, err))
		return nil, ""
	}
	return tag, resID[1]
}

func (a *genericJAASAccessResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	IDstr := req.ID
	resID := strings.Split(IDstr, ":")
	if len(resID) != 2 {
		resp.Diagnostics.AddError(
			"ImportState Failure",
			fmt.Sprintf("Malformed Import ID %q, "+
				"please use format '<resourceTag>:<access>' e.g. %s", IDstr, a.targetResource.ImportHint()),
		)
		return
	}
	_, err := jimmnames.ParseTag(resID[0])
	if err != nil {
		resp.Diagnostics.AddError(
			"ImportState Failure",
			fmt.Sprintf("Malformed Import ID %q, "+
				"%s is not a valid tag", IDstr, resID[0]),
		)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
