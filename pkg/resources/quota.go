package resources

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/helpers"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/internal/provider"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/provider/previewfeatures"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/provider/resources"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/schemas"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// The quota resource is built on the per-user quotas guide (https://docs.snowflake.com/en/user-guide/budgets/per-user-quotas).
// Behaviors the guide doesn't document are marked "TODO [quota spike]" until verified against a live account.

func quotaEnumValuesDescription[T ~string](values []T) string {
	return fmt.Sprintf("Valid values are (case-sensitive): %s.", possibleValuesListed(values))
}

// quotaEnumValidation is case-sensitive on purpose: most enums below live inside sets, where DiffSuppressFunc doesn't apply,
// so a value normalized on read would otherwise produce a permanent diff against a differently cased configuration.
func quotaEnumValidation[T ~string](values []T) schema.SchemaValidateDiagFunc {
	allowed := make([]string, len(values))
	for i, value := range values {
		allowed[i] = string(value)
	}
	return validation.ToDiagFunc(validation.StringInSlice(allowed, false))
}

func quotaTagValueSchema(description string) *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeSet,
		Description: description,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"tag": {
					Type:             schema.TypeString,
					Required:         true,
					Description:      "Fully qualified name of the tag, e.g. `\"<database>\".\"<schema>\".\"<tag>\"`. The role managing the quota needs the APPLYBUDGET privilege on the tag.",
					ValidateDiagFunc: IsValidIdentifier[sdk.SchemaObjectIdentifier](),
				},
				"value": {
					Type:        schema.TypeString,
					Required:    true,
					Description: "Tag value.",
				},
			},
		},
	}
}

func quotaProcedureSchema(description string) *schema.Schema {
	return &schema.Schema{
		Type:             schema.TypeString,
		Required:         true,
		Description:      description,
		ValidateDiagFunc: sdkValidation(sdk.ParseSchemaObjectIdentifierWithArguments),
	}
}

func quotaPerUserLimitSchema(cycle string) *schema.Schema {
	return &schema.Schema{
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      IntDefault,
		ValidateFunc: validation.IntAtLeast(0),
		// TODO [quota spike]: verify whether limits accept decimal credit values.
		Description: fmt.Sprintf("Credit limit applied to each user in scope for every %s cycle (UTC-aligned). If not set, the limit is unset.", cycle),
	}
}

var quotaSchema = map[string]*schema.Schema{
	"name": {
		Type:             schema.TypeString,
		Required:         true,
		ForceNew:         true,
		Description:      blocklistedCharactersFieldDescription("Specifies the identifier for the quota; must be unique for the database and schema in which the quota is created."),
		DiffSuppressFunc: suppressIdentifierQuoting,
	},
	"database": {
		Type:             schema.TypeString,
		Required:         true,
		ForceNew:         true,
		Description:      blocklistedCharactersFieldDescription("The database in which to create the quota."),
		DiffSuppressFunc: suppressIdentifierQuoting,
	},
	"schema": {
		Type:             schema.TypeString,
		Required:         true,
		ForceNew:         true,
		Description:      blocklistedCharactersFieldDescription("The schema in which to create the quota."),
		DiffSuppressFunc: suppressIdentifierQuoting,
	},
	"comment": {
		Type:     schema.TypeString,
		Optional: true,
		// TODO [quota spike]: the guide documents neither COMMENT on CREATE nor a way to change it afterwards.
		ForceNew:    true,
		Description: "Specifies a comment for the quota. Changing it recreates the quota.",
	},
	"shared_resource": {
		Type:     schema.TypeSet,
		Optional: true,
		Description: joinWithSpace(
			"Resources (domains) whose spend counts toward the quota.",
			"A quota can monitor either the WAREHOUSE domain or the AI domains, not both.",
		),
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"domain": {
					Type:             schema.TypeString,
					Required:         true,
					Description:      joinWithSpace("Monitored domain.", quotaEnumValuesDescription(sdk.AllQuotaSharedResourceDomains)),
					ValidateDiagFunc: quotaEnumValidation(sdk.AllQuotaSharedResourceDomains),
				},
				"instance": {
					Type:     schema.TypeString,
					Optional: true,
					Description: joinWithSpace(
						"A single instance of the domain; if not set, the whole domain is monitored.",
						"For `AI FUNCTION` and `CORTEX CODE`, the instance name (e.g. `AI_CLASSIFY`, `CORTEX_CODE_CLI`).",
						"For `CORTEX AGENT`, the fully qualified agent name.",
						"For `WAREHOUSE`, `SNOWFLAKE INTELLIGENCE`, and `AI GATEWAY`, the object name.",
					),
				},
			},
		},
	},
	"user_tags": {
		Type:        schema.TypeList,
		Optional:    true,
		MaxItems:    1,
		Description: "Applies the quota to users with the given tags.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"operator": {
					Type:             schema.TypeString,
					Optional:         true,
					Default:          string(sdk.QuotaUserTagsOperatorUnion),
					Description:      joinWithSpace("How the tags are combined.", quotaEnumValuesDescription(sdk.AllQuotaUserTagsOperators)),
					ValidateDiagFunc: quotaEnumValidation(sdk.AllQuotaUserTagsOperators),
				},
				"tag": func() *schema.Schema {
					s := quotaTagValueSchema("Tag and value pairs that select the users.")
					s.Required = true
					s.MinItems = 1
					return s
				}(),
			},
		},
	},
	"excluded_user_tag": func() *schema.Schema {
		// TODO [quota spike]: GET_QUOTA_SCOPE doesn't tell excluded tags apart from included ones; until it does, external changes are not detected.
		s := quotaTagValueSchema(externalChangesNotDetectedFieldDescription("Excludes users with the given tag and value from the quota."))
		s.Optional = true
		return s
	}(),
	"included_users": {
		Type:        schema.TypeSet,
		Optional:    true,
		Elem:        &schema.Schema{Type: schema.TypeString, DiffSuppressFunc: suppressIdentifierQuoting},
		Description: relatedResourceDescription("Names of individual users the quota applies to.", resources.User),
	},
	"excluded_users": {
		Type:        schema.TypeSet,
		Optional:    true,
		Elem:        &schema.Schema{Type: schema.TypeString, DiffSuppressFunc: suppressIdentifierQuoting},
		Description: relatedResourceDescription("Names of individual users excluded from the quota.", resources.User),
	},
	"per_user_limit_monthly": quotaPerUserLimitSchema("monthly"),
	"per_user_limit_weekly":  quotaPerUserLimitSchema("weekly"),
	"per_user_limit_daily":   quotaPerUserLimitSchema("daily"),
	"block_enforcement_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Default:     false,
		Description: "Blocks new requests of users who reached their limit. Applies only to the AI domains.",
	},
	"notify_blocked_users": {
		Type:     schema.TypeBool,
		Optional: true,
		Default:  false,
		// TODO [quota spike]: check whether GET_CONFIG returns this flag; until then, external changes are not detected.
		Description: externalChangesNotDetectedFieldDescription("Sends an email to users when they are blocked. Used only when `block_enforcement_enabled` is true."),
	},
	"notification_threshold": {
		Type:        schema.TypeSet,
		Optional:    true,
		Description: "Thresholds (percentages of the limit) at which notifications are sent.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"threshold": {
					Type:         schema.TypeInt,
					Required:     true,
					ValidateFunc: validation.IntAtLeast(1),
					Description:  "Percentage of the limit.",
				},
				"spend_strategy": {
					Type:             schema.TypeString,
					Optional:         true,
					Default:          string(sdk.QuotaSpendStrategyProjected),
					Description:      joinWithSpace("Whether the threshold applies to actual or projected spend.", quotaEnumValuesDescription(sdk.AllQuotaSpendStrategies)),
					ValidateDiagFunc: quotaEnumValidation(sdk.AllQuotaSpendStrategies),
				},
				"notify_user": {
					Type:        schema.TypeBool,
					Optional:    true,
					Default:     true,
					Description: "Notifies the user who crossed the threshold.",
				},
				"cycle": {
					Type:             schema.TypeString,
					Optional:         true,
					Default:          string(sdk.QuotaCycleMonthly),
					Description:      joinWithSpace("Cycle the threshold applies to.", quotaEnumValuesDescription(sdk.AllQuotaCycles)),
					ValidateDiagFunc: quotaEnumValidation(sdk.AllQuotaCycles),
				},
			},
		},
	},
	"notification_integrations": {
		Type:     schema.TypeSet,
		Optional: true,
		Elem: &schema.Schema{
			Type:             schema.TypeString,
			ValidateDiagFunc: IsValidIdentifier[sdk.AccountObjectIdentifier](),
			DiffSuppressFunc: suppressIdentifierQuoting,
		},
		Description: relatedResourceDescription("Notification integrations used to send admin summary notifications. USAGE on each integration must be granted to the SNOWFLAKE application.", resources.EmailNotificationIntegration),
	},
	"admin_emails": {
		Type:     schema.TypeSet,
		Optional: true,
		Elem:     &schema.Schema{Type: schema.TypeString},
		// TODO [quota spike]: there is no documented getter for admin emails; until one is found, external changes are not detected.
		Description: externalChangesNotDetectedFieldDescription("Verified email addresses that receive admin summary notifications."),
	},
	"custom_action": {
		Type:        schema.TypeSet,
		Optional:    true,
		Description: "Stored procedures called when users cross a threshold of their monthly limit.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"procedure": quotaProcedureSchema(joinWithSpace(
					"Fully qualified name of the procedure with its argument types, e.g. `\"<database>\".\"<schema>\".\"<procedure>\"(VARCHAR)`.",
					"The procedure must use EXECUTE AS OWNER and be granted to the SNOWFLAKE application; its first argument receives the IDs of the users who crossed the threshold.",
				)),
				"arguments": {
					Type:        schema.TypeList,
					Optional:    true,
					Elem:        &schema.Schema{Type: schema.TypeString},
					Description: "Additional arguments passed to the procedure.",
				},
				"spend_strategy": {
					Type:             schema.TypeString,
					Optional:         true,
					Default:          string(sdk.QuotaSpendStrategyActual),
					Description:      joinWithSpace("Whether the threshold applies to actual or projected spend.", quotaEnumValuesDescription(sdk.AllQuotaSpendStrategies)),
					ValidateDiagFunc: quotaEnumValidation(sdk.AllQuotaSpendStrategies),
				},
				"threshold": {
					Type:         schema.TypeInt,
					Required:     true,
					ValidateFunc: validation.IntAtLeast(1),
					Description:  "Percentage of the monthly limit.",
				},
			},
		},
	},
	"cycle_start_action": {
		Type:        schema.TypeList,
		Optional:    true,
		MaxItems:    1,
		Description: "Stored procedure called at the start of each monthly cycle.",
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"procedure": quotaProcedureSchema(joinWithSpace(
					"Fully qualified name of the procedure with its argument types, e.g. `\"<database>\".\"<schema>\".\"<procedure>\"()`.",
					"The procedure must use EXECUTE AS OWNER and be granted to the SNOWFLAKE application.",
				)),
				"arguments": {
					Type:        schema.TypeList,
					Optional:    true,
					Elem:        &schema.Schema{Type: schema.TypeString},
					Description: "Arguments passed to the procedure.",
				},
			},
		},
	},
	ShowOutputAttributeName: {
		Type:        schema.TypeList,
		Computed:    true,
		Description: "Outputs the result of `SHOW SNOWFLAKE.CORE.QUOTA` for the given quota.",
		Elem: &schema.Resource{
			Schema: schemas.ShowQuotaSchema,
		},
	},
	DescribeOutputAttributeName: {
		Type:        schema.TypeList,
		Computed:    true,
		Description: "Outputs the quota configuration read with its GET_* methods (scope, config, notifications, and actions).",
		Elem: &schema.Resource{
			Schema: schemas.DescribeQuotaDetailsSchema,
		},
	},
	FullyQualifiedNameAttributeName: schemas.FullyQualifiedNameSchema,
}

func Quota() *schema.Resource {
	deleteFunc := ResourceDeleteContextFunc(
		sdk.ParseSchemaObjectIdentifier,
		func(client *sdk.Client) DropSafelyFunc[sdk.SchemaObjectIdentifier] {
			return client.Quotas.DropSafely
		},
	)

	return &schema.Resource{
		CreateContext: PreviewFeatureCreateContextWrapper(string(previewfeatures.QuotaResource), TrackingCreateWrapper(resources.Quota, CreateQuota)),
		ReadContext:   PreviewFeatureReadContextWrapper(string(previewfeatures.QuotaResource), TrackingReadWrapper(resources.Quota, ReadQuota)),
		UpdateContext: PreviewFeatureUpdateContextWrapper(string(previewfeatures.QuotaResource), TrackingUpdateWrapper(resources.Quota, UpdateQuota)),
		DeleteContext: PreviewFeatureDeleteContextWrapper(string(previewfeatures.QuotaResource), TrackingDeleteWrapper(resources.Quota, deleteFunc)),
		Description: joinWithSpace(
			"Resource used to manage per-user quota objects (`SNOWFLAKE.CORE.QUOTA`).",
			"For more information, check [per-user quotas documentation](https://docs.snowflake.com/en/user-guide/budgets/per-user-quotas).",
		),

		Schema: quotaSchema,
		Importer: &schema.ResourceImporter{
			StateContext: TrackingImportWrapper(resources.Quota, ImportName[sdk.SchemaObjectIdentifier]),
		},

		CustomizeDiff: TrackingCustomDiffWrapper(resources.Quota, customdiff.All(
			validateQuotaDomains,
			validateQuotaNotificationThresholds,
			ComputedIfAnyAttributeChanged(quotaSchema, ShowOutputAttributeName, "comment"),
			// Set and list blocks are excluded from describe_output invalidation, because they produce false-positive
			// HasChange results in ComputedIfAnyAttributeChanged. describe_output is still refreshed after every apply.
			// TODO [SNOW-1648997]: address the above comment (same pattern as external_access_integration)
			ComputedIfAnyAttributeChanged(
				quotaSchema, DescribeOutputAttributeName,
				"included_users",
				"excluded_users",
				"per_user_limit_monthly",
				"per_user_limit_weekly",
				"per_user_limit_daily",
				"block_enforcement_enabled",
				"notification_integrations",
			),
			ComputedIfAnyAttributeChanged(quotaSchema, FullyQualifiedNameAttributeName, "name", "schema", "database"),
		)),
		Timeouts: defaultTimeouts,
	}
}

// validateQuotaDomains rejects configurations Snowflake documents as unsupported but doesn't validate itself.
func validateQuotaDomains(_ context.Context, d *schema.ResourceDiff, _ any) error {
	if !d.NewValueKnown("shared_resource") {
		return nil
	}
	var errs []error
	var monitorsWarehouses, monitorsAi bool
	for _, raw := range d.Get("shared_resource").(*schema.Set).List() {
		resource := raw.(map[string]any)
		if resource["domain"].(string) == "" {
			// an unknown domain (e.g. a reference to another resource) is read as an empty string; it is validated at apply time
			continue
		}
		domain, err := sdk.ToQuotaSharedResourceDomain(resource["domain"].(string))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := sdk.NewQuotaAddSharedResourceArgsRequestFromInstance(domain, resource["instance"].(string)); err != nil {
			errs = append(errs, fmt.Errorf("invalid instance %q of the %s domain: %w", resource["instance"], domain, err))
		}
		if domain == sdk.QuotaSharedResourceDomainWarehouse {
			monitorsWarehouses = true
		} else {
			monitorsAi = true
		}
	}
	if monitorsWarehouses && monitorsAi {
		errs = append(errs, errors.New("a quota can monitor either the WAREHOUSE domain or the AI domains, not both"))
	}
	if monitorsWarehouses && d.Get("block_enforcement_enabled").(bool) {
		errs = append(errs, errors.New("block_enforcement_enabled applies only to the AI domains and can't be used with the WAREHOUSE domain"))
	}
	return errors.Join(errs...)
}

// validateQuotaNotificationThresholds rejects thresholds Snowflake can't tell apart: it keeps one per (threshold, spend strategy).
func validateQuotaNotificationThresholds(_ context.Context, d *schema.ResourceDiff, _ any) error {
	if !d.NewValueKnown("notification_threshold") {
		return nil
	}
	seen := make(map[string]struct{})
	for _, raw := range d.Get("notification_threshold").(*schema.Set).List() {
		threshold := raw.(map[string]any)
		key := fmt.Sprintf("%d%% %s", threshold["threshold"].(int), strings.ToUpper(threshold["spend_strategy"].(string)))
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("notification_threshold: Snowflake keeps one threshold per threshold and spend_strategy, but %s is defined more than once", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func CreateQuota(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*provider.Context).Client
	id := sdk.NewSchemaObjectIdentifier(d.Get("database").(string), d.Get("schema").(string), d.Get("name").(string))

	desired, err := quotaConfigurationFromRaw(d.Get)
	if err != nil {
		return diag.FromErr(err)
	}

	request := sdk.NewCreateQuotaRequest(id)
	if err := stringAttributeCreateBuilder(d, "comment", request.WithComment); err != nil {
		return diag.FromErr(err)
	}
	if err := client.Quotas.Create(ctx, request); err != nil {
		return diag.FromErr(fmt.Errorf("error creating quota %v, err = %w", id.FullyQualifiedName(), err))
	}
	d.SetId(helpers.EncodeResourceIdentifier(id))

	if err := reconcileQuota(ctx, client, id, &quotaConfiguration{}, desired); err != nil {
		return diag.FromErr(fmt.Errorf("error configuring quota %v, err = %w", id.FullyQualifiedName(), err))
	}

	return ReadQuota(ctx, d, meta)
}

func ReadQuota(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*provider.Context).Client
	id, err := sdk.ParseSchemaObjectIdentifier(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	quota, err := client.Quotas.ShowByIDSafely(ctx, id)
	if err != nil {
		if errors.Is(err, sdk.ErrObjectNotFound) {
			d.SetId("")
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "Failed to query quota. Marking the resource as removed.",
					Detail:   fmt.Sprintf("Quota id: %s, Err: %s", id.FullyQualifiedName(), err),
				},
			}
		}
		return diag.FromErr(err)
	}

	details, err := client.Quotas.DescribeDetails(ctx, id)
	if err != nil {
		return diag.FromErr(err)
	}

	// The configuration currently in the state is used to keep the user's spelling of equivalent values
	// (e.g. quoted vs. unquoted identifiers) and to fill in the values that can't be read back.
	current, err := quotaConfigurationFromRaw(d.Get)
	if err != nil {
		return diag.FromErr(err)
	}
	remote, err := quotaConfigurationFromDetails(details, current)
	if err != nil {
		return diag.FromErr(err)
	}

	comment := ""
	if quota.Comment != nil {
		comment = *quota.Comment
	}

	errs := errors.Join(
		d.Set(FullyQualifiedNameAttributeName, id.FullyQualifiedName()),
		d.Set("comment", comment),
		d.Set("shared_resource", remote.sharedResourcesToSchema()),
		d.Set("user_tags", remote.userTagsToSchema()),
		d.Set("excluded_user_tag", quotaTagValuesToSchema(remote.excludedUserTags)),
		d.Set("included_users", remote.includedUsers),
		d.Set("excluded_users", remote.excludedUsers),
		d.Set("per_user_limit_monthly", remote.perUserLimit(sdk.QuotaCycleMonthly)),
		d.Set("per_user_limit_weekly", remote.perUserLimit(sdk.QuotaCycleWeekly)),
		d.Set("per_user_limit_daily", remote.perUserLimit(sdk.QuotaCycleDaily)),
		d.Set("block_enforcement_enabled", remote.blockEnforcementEnabled),
		// not readable from Snowflake: kept from the configuration, and set to their defaults on import
		d.Set("notify_blocked_users", remote.notifyBlockedUsers),
		d.Set("admin_emails", remote.adminEmails),
		d.Set("notification_threshold", remote.notificationThresholdsToSchema()),
		d.Set("notification_integrations", remote.notificationIntegrationsToSchema()),
		d.Set("custom_action", remote.customActionsToSchema()),
		d.Set("cycle_start_action", remote.cycleStartActionToSchema()),
		d.Set(ShowOutputAttributeName, []map[string]any{schemas.QuotaToSchema(quota)}),
		d.Set(DescribeOutputAttributeName, []map[string]any{schemas.QuotaDetailsToSchema(details)}),
	)
	return diag.FromErr(errs)
}

func UpdateQuota(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	client := meta.(*provider.Context).Client
	id, err := sdk.ParseSchemaObjectIdentifier(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	previous, err := quotaConfigurationFromRaw(func(key string) any {
		oldValue, _ := d.GetChange(key)
		return oldValue
	})
	if err != nil {
		return diag.FromErr(err)
	}
	desired, err := quotaConfigurationFromRaw(d.Get)
	if err != nil {
		return diag.FromErr(err)
	}

	if err := reconcileQuota(ctx, client, id, previous, desired); err != nil {
		// On a failed update the state is saved with the planned values. The values that can be read back are corrected by the
		// next refresh; the ones that can't are restored here, so that the next plan applies them again.
		var errs []error
		for _, key := range []string{"admin_emails", "notify_blocked_users", "excluded_user_tag", "notification_threshold"} {
			previousValue, _ := d.GetChange(key)
			errs = append(errs, d.Set(key, previousValue))
		}
		return diag.FromErr(errors.Join(fmt.Errorf("error updating quota %v, err = %w", id.FullyQualifiedName(), err), errors.Join(errs...)))
	}

	return ReadQuota(ctx, d, meta)
}

// reconcileQuota issues the instance method calls that turn the previous quota configuration into the desired one.
// Removals go first, so that e.g. switching between the WAREHOUSE and AI domains works. Block enforcement is disabled before
// any other change (it can't be combined with the WAREHOUSE domain) and enabled after all of them, so that users aren't blocked
// by a partially applied configuration. The calls aren't transactional: if one fails, the quota is left partially configured.
func reconcileQuota(ctx context.Context, client *sdk.Client, id sdk.SchemaObjectIdentifier, previous *quotaConfiguration, desired *quotaConfiguration) error {
	quotas := client.Quotas

	blockEnforcementChanged := previous.blockEnforcementEnabled != desired.blockEnforcementEnabled || previous.notifyBlockedUsers != desired.notifyBlockedUsers
	setBlockEnforcement := func() error {
		args := sdk.NewQuotaSetBlockEnforcementEnabledArgsRequest(desired.blockEnforcementEnabled).WithNotifyUsers(desired.notifyBlockedUsers)
		if _, err := quotas.SetBlockEnforcementEnabled(ctx, sdk.NewSetBlockEnforcementEnabledQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("setting block enforcement: %w", err)
		}
		return nil
	}
	if blockEnforcementChanged && !desired.blockEnforcementEnabled {
		if err := setBlockEnforcement(); err != nil {
			return err
		}
	}

	// REMOVE_CUSTOM_ACTIONS(<threshold>, <procedure>) removes the actions for both spend strategies,
	// so the desired actions sharing a removed (threshold, procedure) pair are added back afterwards.
	removedActions, addedActions := quotaDiffByKey(previous.customActions, desired.customActions, quotaCustomAction.key)
	removedActionGroups := make(map[string]quotaCustomAction)
	for _, action := range removedActions {
		removedActionGroups[action.removalKey()] = action
	}
	if len(removedActionGroups) > 0 {
		// the registered actions are read first, so that the procedure is passed back exactly as Snowflake returns it
		registered, err := quotas.GetCustomActions(ctx, sdk.NewGetCustomActionsQuotaRequest(id))
		if err != nil {
			return fmt.Errorf("reading custom actions: %w", err)
		}
		for _, key := range slices.Sorted(maps.Keys(removedActionGroups)) {
			action := removedActionGroups[key]
			procedureFqn, found := quotaRegisteredProcedureFqn(registered, action)
			if !found {
				continue
			}
			args := sdk.NewQuotaRemoveCustomActionsArgsRequest().WithThreshold(action.threshold).WithProcedureFqn(procedureFqn)
			if _, err := quotas.RemoveCustomActions(ctx, sdk.NewRemoveCustomActionsQuotaRequest(id, *args)); err != nil {
				return fmt.Errorf("removing custom actions at %d%% for %s: %w", action.threshold, action.procedure.FullyQualifiedName(), err)
			}
		}
	}
	for _, action := range desired.customActions {
		if _, ok := removedActionGroups[action.removalKey()]; ok && !slices.ContainsFunc(addedActions, func(a quotaCustomAction) bool { return a.key() == action.key() }) {
			addedActions = append(addedActions, action)
		}
	}

	removedThresholds, addedThresholds := quotaDiffByKey(previous.notificationThresholds, desired.notificationThresholds, quotaNotificationThreshold.key)
	for _, threshold := range removedThresholds {
		args := sdk.NewQuotaRemoveNotificationThresholdArgsRequest(threshold.threshold, threshold.spendStrategy)
		if _, err := quotas.RemoveNotificationThreshold(ctx, sdk.NewRemoveNotificationThresholdQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("removing notification threshold %d%% (%s): %w", threshold.threshold, threshold.spendStrategy, err)
		}
	}

	removedIntegrations, addedIntegrations := quotaDiffByKey(previous.notificationIntegrations, desired.notificationIntegrations, quotaNotificationIntegration.key)
	for _, integration := range removedIntegrations {
		args := sdk.NewQuotaRemoveNotificationIntegrationArgsRequest(integration.id)
		if _, err := quotas.RemoveNotificationIntegration(ctx, sdk.NewRemoveNotificationIntegrationQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("removing notification integration %s: %w", integration.id.FullyQualifiedName(), err)
		}
	}

	removedResources, addedResources := quotaDiffByKey(previous.sharedResources, desired.sharedResources, quotaSharedResource.key)
	for _, resource := range removedResources {
		args, err := sdk.NewQuotaRemoveSharedResourceArgsRequestFromInstance(resource.domain, resource.instance)
		if err != nil {
			return err
		}
		if _, err := quotas.RemoveSharedResource(ctx, sdk.NewRemoveSharedResourceQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("removing shared resource %s: %w", resource.description(), err)
		}
	}
	for _, resource := range addedResources {
		args, err := sdk.NewQuotaAddSharedResourceArgsRequestFromInstance(resource.domain, resource.instance)
		if err != nil {
			return err
		}
		if _, err := quotas.AddSharedResource(ctx, sdk.NewAddSharedResourceQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("adding shared resource %s: %w", resource.description(), err)
		}
	}

	// SET_USER_TAGS overwrites the tags, so the whole desired list is sent; the operator is always passed (UNION is Snowflake's default).
	if !previous.userTagsEqual(desired) {
		// TODO [quota spike]: verify that an empty array clears the user tags.
		tags := make([]sdk.QuotaUserTagRequest, len(desired.userTags))
		for i, tag := range desired.userTags {
			tags[i] = *sdk.NewQuotaUserTagRequest(tag.tag, tag.value)
		}
		operator := sdk.QuotaUserTagsOperatorUnion
		if desired.userTagsOperator != nil {
			operator = *desired.userTagsOperator
		}
		args := sdk.NewQuotaSetUserTagsArgsRequest(tags).WithOperator(operator)
		if _, err := quotas.SetUserTags(ctx, sdk.NewSetUserTagsQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("setting user tags: %w", err)
		}
	}
	// INCLUDE_USERS and EXCLUDE_USERS accumulate; see quotaReconcileAccumulatingList.
	excludeUsersByTag := func(tags []quotaTagValue) error {
		requests := make([]sdk.QuotaExcludedUserTagRequest, len(tags))
		for i, tag := range tags {
			requests[i] = *sdk.NewQuotaExcludedUserTagRequest(tag.tag, tag.value)
		}
		args := sdk.NewQuotaExcludeUsersArgsRequest().WithByTag(*sdk.NewQuotaExcludeUsersByTagRequest(requests))
		if _, err := quotas.ExcludeUsers(ctx, sdk.NewExcludeUsersQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("excluding users by tag: %w", err)
		}
		return nil
	}
	if err := quotaReconcileAccumulatingList(previous.excludedUserTags, desired.excludedUserTags, quotaTagValue.key, excludeUsersByTag); err != nil {
		return err
	}
	includeUsers := func(users []string) error {
		args := sdk.NewQuotaIncludeUsersArgsRequest(users)
		if _, err := quotas.IncludeUsers(ctx, sdk.NewIncludeUsersQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("including users: %w", err)
		}
		return nil
	}
	if err := quotaReconcileAccumulatingList(previous.includedUsers, desired.includedUsers, quotaUserKey, includeUsers); err != nil {
		return err
	}
	excludeUsers := func(users []string) error {
		args := sdk.NewQuotaExcludeUsersArgsRequest().WithByUser(*sdk.NewQuotaExcludeUsersByUserRequest(users))
		if _, err := quotas.ExcludeUsers(ctx, sdk.NewExcludeUsersQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("excluding users: %w", err)
		}
		return nil
	}
	if err := quotaReconcileAccumulatingList(previous.excludedUsers, desired.excludedUsers, quotaUserKey, excludeUsers); err != nil {
		return err
	}

	for _, cycle := range sdk.AllQuotaCycles {
		previousLimit, desiredLimit := previous.perUserLimit(cycle), desired.perUserLimit(cycle)
		if previousLimit == desiredLimit {
			continue
		}
		if desiredLimit == IntDefault {
			// TODO [quota spike]: the guide only shows UNSET_PER_USER_LIMIT('WEEKLY'); verify the monthly cycle.
			args := sdk.NewQuotaUnsetPerUserLimitArgsRequest(cycle)
			if _, err := quotas.UnsetPerUserLimit(ctx, sdk.NewUnsetPerUserLimitQuotaRequest(id, *args)); err != nil {
				return fmt.Errorf("unsetting the %s per-user limit: %w", cycle, err)
			}
			continue
		}
		args := sdk.NewQuotaSetPerUserLimitArgsRequest(desiredLimit)
		if cycle != sdk.QuotaCycleMonthly {
			args.WithCycle(cycle)
		}
		if _, err := quotas.SetPerUserLimit(ctx, sdk.NewSetPerUserLimitQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("setting the %s per-user limit: %w", cycle, err)
		}
	}

	for _, integration := range addedIntegrations {
		args := sdk.NewQuotaAddNotificationIntegrationArgsRequest(integration.id)
		if _, err := quotas.AddNotificationIntegration(ctx, sdk.NewAddNotificationIntegrationQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("adding notification integration %s: %w", integration.id.FullyQualifiedName(), err)
		}
	}
	if !quotaKeysEqual(previous.adminEmails, desired.adminEmails, quotaEmailKey) {
		// TODO [quota spike]: verify that an empty string clears the admin emails.
		args := sdk.NewQuotaSetAdminEmailsArgsRequestFromEmails(desired.adminEmails...)
		if _, err := quotas.SetAdminEmails(ctx, sdk.NewSetAdminEmailsQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("setting admin emails: %w", err)
		}
	}
	for _, threshold := range addedThresholds {
		args := sdk.NewQuotaAddNotificationThresholdArgsRequest(threshold.threshold, threshold.spendStrategy, threshold.notifyUser, threshold.cycle)
		if _, err := quotas.AddNotificationThreshold(ctx, sdk.NewAddNotificationThresholdQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("adding notification threshold %d%% (%s): %w", threshold.threshold, threshold.spendStrategy, err)
		}
	}

	for _, action := range addedActions {
		args := sdk.NewQuotaAddCustomActionArgsRequest(action.procedure, action.arguments, action.spendStrategy, action.threshold)
		if _, err := quotas.AddCustomAction(ctx, sdk.NewAddCustomActionQuotaRequest(id, *args)); err != nil {
			return fmt.Errorf("adding custom action at %d%% for %s: %w", action.threshold, action.procedure.FullyQualifiedName(), err)
		}
	}
	if previous.cycleStartActionKey() != desired.cycleStartActionKey() {
		if desired.cycleStartAction == nil {
			if _, err := quotas.RemoveCycleStartAction(ctx, sdk.NewRemoveCycleStartActionQuotaRequest(id)); err != nil {
				return fmt.Errorf("removing the cycle-start action: %w", err)
			}
		} else {
			args := sdk.NewQuotaSetCycleStartActionArgsRequest(desired.cycleStartAction.procedure, desired.cycleStartAction.arguments)
			if _, err := quotas.SetCycleStartAction(ctx, sdk.NewSetCycleStartActionQuotaRequest(id, *args)); err != nil {
				return fmt.Errorf("setting the cycle-start action: %w", err)
			}
		}
	}

	if blockEnforcementChanged && desired.blockEnforcementEnabled {
		if err := setBlockEnforcement(); err != nil {
			return err
		}
	}

	return nil
}
