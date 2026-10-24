package resources

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// quotaConfiguration is the declarative configuration of a quota, built either from the Terraform configuration/state
// or from the quota's GET_* methods. Elements keep the spelling they were read with ("raw" fields), and are compared
// by normalized keys, so that equivalent spellings (e.g. quoted and unquoted identifiers) don't produce diffs.
type quotaConfiguration struct {
	sharedResources          []quotaSharedResource
	userTagsOperator         *sdk.QuotaUserTagsOperator
	userTags                 []quotaTagValue
	excludedUserTags         []quotaTagValue
	includedUsers            []string
	excludedUsers            []string
	perUserLimits            map[sdk.QuotaCycle]int
	blockEnforcementEnabled  bool
	notifyBlockedUsers       bool
	notificationThresholds   []quotaNotificationThreshold
	notificationIntegrations []quotaNotificationIntegration
	adminEmails              []string
	customActions            []quotaCustomAction
	cycleStartAction         *quotaCycleStartAction
}

type quotaSharedResource struct {
	domain   sdk.QuotaSharedResourceDomain
	instance string
}

func (r quotaSharedResource) key() string {
	instance := r.instance
	switch {
	case instance == "":
	case slices.Contains([]sdk.QuotaSharedResourceDomain{sdk.QuotaSharedResourceDomainAiFunction, sdk.QuotaSharedResourceDomainCortexCode}, r.domain):
		instance = strings.ToUpper(instance)
	case r.domain == sdk.QuotaSharedResourceDomainCortexAgent:
		if id, err := sdk.ParseSchemaObjectIdentifier(instance); err == nil {
			instance = id.FullyQualifiedName()
		}
	default:
		if id, err := sdk.ParseAccountObjectIdentifier(instance); err == nil {
			instance = id.FullyQualifiedName()
		}
	}
	return string(r.domain) + "/" + instance
}

func (r quotaSharedResource) description() string {
	if r.instance == "" {
		return string(r.domain)
	}
	return fmt.Sprintf("%s (%s)", r.domain, r.instance)
}

type quotaTagValue struct {
	tag   sdk.SchemaObjectIdentifier
	raw   string
	value string
}

func (t quotaTagValue) key() string {
	return t.tag.FullyQualifiedName() + "=" + t.value
}

type quotaNotificationThreshold struct {
	threshold     int
	spendStrategy sdk.QuotaSpendStrategy
	notifyUser    bool
	cycle         sdk.QuotaCycle
}

func (t quotaNotificationThreshold) key() string {
	return fmt.Sprintf("%d/%s/%t/%s", t.threshold, t.spendStrategy, t.notifyUser, t.cycle)
}

type quotaNotificationIntegration struct {
	id  sdk.AccountObjectIdentifier
	raw string
}

func (i quotaNotificationIntegration) key() string {
	return i.id.FullyQualifiedName()
}

type quotaCustomAction struct {
	procedure     sdk.SchemaObjectIdentifierWithArguments
	raw           string
	arguments     []string
	spendStrategy sdk.QuotaSpendStrategy
	threshold     int
}

func (a quotaCustomAction) key() string {
	return fmt.Sprintf("%s/%s/%q", a.removalKey(), a.spendStrategy, a.arguments)
}

// removalKey identifies the actions removed together by REMOVE_CUSTOM_ACTIONS(<threshold>, <procedure>).
func (a quotaCustomAction) removalKey() string {
	return fmt.Sprintf("%d/%s", a.threshold, a.procedure.FullyQualifiedName())
}

type quotaCycleStartAction struct {
	procedure sdk.SchemaObjectIdentifierWithArguments
	raw       string
	arguments []string
}

func quotaUserKey(name string) string {
	return strings.Trim(name, `"`)
}

func quotaEmailKey(email string) string {
	return strings.ToLower(email)
}

func (c *quotaConfiguration) perUserLimit(cycle sdk.QuotaCycle) int {
	if limit, ok := c.perUserLimits[cycle]; ok {
		return limit
	}
	return IntDefault
}

func (c *quotaConfiguration) userTagsEqual(other *quotaConfiguration) bool {
	operator := func(c *quotaConfiguration) string {
		if c.userTagsOperator == nil {
			return ""
		}
		return string(*c.userTagsOperator)
	}
	return operator(c) == operator(other) && quotaKeysEqual(c.userTags, other.userTags, quotaTagValue.key)
}

func (c *quotaConfiguration) cycleStartActionKey() string {
	if c.cycleStartAction == nil {
		return ""
	}
	return fmt.Sprintf("%s/%q", c.cycleStartAction.procedure.FullyQualifiedName(), c.cycleStartAction.arguments)
}

// quotaConfigurationFromRaw reads the quota configuration with the given getter (d.Get, or the old values of d.GetChange).
func quotaConfigurationFromRaw(get func(key string) any) (*quotaConfiguration, error) {
	config := &quotaConfiguration{perUserLimits: make(map[sdk.QuotaCycle]int)}
	var err error

	for _, raw := range get("shared_resource").(*schema.Set).List() {
		resource := raw.(map[string]any)
		domain, err := sdk.ToQuotaSharedResourceDomain(resource["domain"].(string))
		if err != nil {
			return nil, err
		}
		config.sharedResources = append(config.sharedResources, quotaSharedResource{domain: domain, instance: resource["instance"].(string)})
	}

	if userTags := get("user_tags").([]any); len(userTags) > 0 && userTags[0] != nil {
		block := userTags[0].(map[string]any)
		operator, err := sdk.ToQuotaUserTagsOperator(block["operator"].(string))
		if err != nil {
			return nil, err
		}
		config.userTagsOperator = &operator
		if config.userTags, err = quotaTagValuesFromRaw(block["tag"].(*schema.Set)); err != nil {
			return nil, err
		}
	}
	if config.excludedUserTags, err = quotaTagValuesFromRaw(get("excluded_user_tag").(*schema.Set)); err != nil {
		return nil, err
	}

	config.includedUsers = expandStringList(get("included_users").(*schema.Set).List())
	config.excludedUsers = expandStringList(get("excluded_users").(*schema.Set).List())

	config.perUserLimits[sdk.QuotaCycleMonthly] = get("per_user_limit_monthly").(int)
	config.perUserLimits[sdk.QuotaCycleWeekly] = get("per_user_limit_weekly").(int)
	config.perUserLimits[sdk.QuotaCycleDaily] = get("per_user_limit_daily").(int)

	config.blockEnforcementEnabled = get("block_enforcement_enabled").(bool)
	config.notifyBlockedUsers = get("notify_blocked_users").(bool)

	for _, raw := range get("notification_threshold").(*schema.Set).List() {
		threshold := raw.(map[string]any)
		spendStrategy, err := sdk.ToQuotaSpendStrategy(threshold["spend_strategy"].(string))
		if err != nil {
			return nil, err
		}
		cycle, err := sdk.ToQuotaCycle(threshold["cycle"].(string))
		if err != nil {
			return nil, err
		}
		config.notificationThresholds = append(config.notificationThresholds, quotaNotificationThreshold{
			threshold:     threshold["threshold"].(int),
			spendStrategy: spendStrategy,
			notifyUser:    threshold["notify_user"].(bool),
			cycle:         cycle,
		})
	}

	for _, raw := range expandStringList(get("notification_integrations").(*schema.Set).List()) {
		id, err := sdk.ParseAccountObjectIdentifier(raw)
		if err != nil {
			return nil, err
		}
		config.notificationIntegrations = append(config.notificationIntegrations, quotaNotificationIntegration{id: id, raw: raw})
	}

	config.adminEmails = expandStringList(get("admin_emails").(*schema.Set).List())

	for _, raw := range get("custom_action").(*schema.Set).List() {
		action := raw.(map[string]any)
		procedure, err := sdk.ParseSchemaObjectIdentifierWithArguments(action["procedure"].(string))
		if err != nil {
			return nil, err
		}
		spendStrategy, err := sdk.ToQuotaSpendStrategy(action["spend_strategy"].(string))
		if err != nil {
			return nil, err
		}
		config.customActions = append(config.customActions, quotaCustomAction{
			procedure:     procedure,
			raw:           action["procedure"].(string),
			arguments:     expandStringListAllowEmpty(action["arguments"].([]any)),
			spendStrategy: spendStrategy,
			threshold:     action["threshold"].(int),
		})
	}

	if cycleStartAction := get("cycle_start_action").([]any); len(cycleStartAction) > 0 && cycleStartAction[0] != nil {
		action := cycleStartAction[0].(map[string]any)
		procedure, err := sdk.ParseSchemaObjectIdentifierWithArguments(action["procedure"].(string))
		if err != nil {
			return nil, err
		}
		config.cycleStartAction = &quotaCycleStartAction{
			procedure: procedure,
			raw:       action["procedure"].(string),
			arguments: expandStringListAllowEmpty(action["arguments"].([]any)),
		}
	}

	return config, nil
}

func quotaTagValuesFromRaw(set *schema.Set) ([]quotaTagValue, error) {
	tags := make([]quotaTagValue, 0, set.Len())
	for _, raw := range set.List() {
		tagValue := raw.(map[string]any)
		tagId, err := sdk.ParseSchemaObjectIdentifier(tagValue["tag"].(string))
		if err != nil {
			return nil, err
		}
		tags = append(tags, quotaTagValue{tag: tagId, raw: tagValue["tag"].(string), value: tagValue["value"].(string)})
	}
	return tags, nil
}

// quotaConfigurationFromDetails converts the quota's GET_* results into its declarative configuration.
// Values equivalent to the current configuration keep its spelling; values that can't be read back are taken from it.
func quotaConfigurationFromDetails(details *sdk.QuotaDetails, current *quotaConfiguration) *quotaConfiguration {
	remote := &quotaConfiguration{
		perUserLimits:      make(map[sdk.QuotaCycle]int),
		notifyBlockedUsers: current.notifyBlockedUsers,
		adminEmails:        current.adminEmails,
	}

	for _, resource := range details.Scope.SharedResources {
		sharedResource := quotaSharedResource{domain: normalizeQuotaDomain(resource.Domain)}
		if !resource.IsWholeDomain() {
			sharedResource.instance = resource.Name
		}
		remote.sharedResources = append(remote.sharedResources, preferCurrentQuotaValue(current.sharedResources, sharedResource, quotaSharedResource.key))
	}

	if userTags := details.Scope.UserTags; userTags != nil {
		for _, tag := range userTags.Tags {
			// TODO [quota spike]: verify the "type" values that distinguish included and excluded tags.
			excluded := strings.Contains(strings.ToUpper(tag.Type), "EXCLU")
			for _, value := range tag.TagValues {
				tagValue := quotaTagValue{tag: tag.TagId(), raw: tag.TagId().FullyQualifiedName(), value: value}
				if excluded {
					remote.excludedUserTags = append(remote.excludedUserTags, preferCurrentQuotaValue(current.excludedUserTags, tagValue, quotaTagValue.key))
				} else {
					remote.userTags = append(remote.userTags, preferCurrentQuotaValue(current.userTags, tagValue, quotaTagValue.key))
				}
			}
		}
		if len(remote.userTags) > 0 {
			operator, err := sdk.ToQuotaUserTagsOperator(userTags.Operator)
			if err != nil {
				operator = sdk.QuotaUserTagsOperatorUnion
			}
			remote.userTagsOperator = &operator
		}
	}

	for _, user := range details.Scope.IncludedUsers {
		remote.includedUsers = append(remote.includedUsers, preferCurrentQuotaValue(current.includedUsers, user.UserName, quotaUserKey))
	}
	for _, user := range details.Scope.ExcludedUsers {
		remote.excludedUsers = append(remote.excludedUsers, preferCurrentQuotaValue(current.excludedUsers, user.UserName, quotaUserKey))
	}

	for cycle, limit := range map[sdk.QuotaCycle]*float64{
		sdk.QuotaCycleMonthly: details.Config.PerUserLimit,
		sdk.QuotaCycleWeekly:  details.Config.PerUserLimitWeekly,
		sdk.QuotaCycleDaily:   details.Config.PerUserLimitDaily,
	} {
		if limit != nil {
			remote.perUserLimits[cycle] = int(*limit)
		}
	}
	if details.Config.BlockEnforcementEnabled != nil {
		remote.blockEnforcementEnabled = *details.Config.BlockEnforcementEnabled
	}

	for _, threshold := range details.NotificationThresholds {
		// TODO [quota spike]: GET_NOTIFICATION_THRESHOLDS doesn't document the cycle; it's taken from the current configuration.
		cycle := sdk.QuotaCycleMonthly
		if idx := slices.IndexFunc(current.notificationThresholds, func(t quotaNotificationThreshold) bool {
			return t.threshold == threshold.Threshold && t.spendStrategy == threshold.SpendStrategy
		}); idx >= 0 {
			cycle = current.notificationThresholds[idx].cycle
		}
		remote.notificationThresholds = append(remote.notificationThresholds, quotaNotificationThreshold{
			threshold:     threshold.Threshold,
			spendStrategy: threshold.SpendStrategy,
			notifyUser:    threshold.NotifyUser,
			cycle:         cycle,
		})
	}

	for _, integration := range details.NotificationIntegrations {
		id, err := sdk.ParseAccountObjectIdentifier(integration.IntegrationName)
		if err != nil {
			id = sdk.NewAccountObjectIdentifier(integration.IntegrationName)
		}
		remoteIntegration := quotaNotificationIntegration{id: id, raw: integration.IntegrationName}
		remote.notificationIntegrations = append(remote.notificationIntegrations, preferCurrentQuotaValue(current.notificationIntegrations, remoteIntegration, quotaNotificationIntegration.key))
	}

	for _, action := range details.CustomActions {
		remoteAction := quotaCustomAction{
			procedure:     action.ProcedureId,
			raw:           action.ProcedureId.FullyQualifiedName(),
			arguments:     action.ProcedureArgs,
			spendStrategy: action.SpendStrategy,
			threshold:     action.Threshold,
		}
		remote.customActions = append(remote.customActions, preferCurrentQuotaValue(current.customActions, remoteAction, quotaCustomAction.key))
	}

	if action := details.CycleStartAction; action != nil && action.ProcedureId != nil {
		remote.cycleStartAction = &quotaCycleStartAction{
			procedure: *action.ProcedureId,
			raw:       action.ProcedureId.FullyQualifiedName(),
			arguments: action.ProcedureArgs,
		}
		if current.cycleStartAction != nil && current.cycleStartActionKey() == remote.cycleStartActionKey() {
			remote.cycleStartAction.raw = current.cycleStartAction.raw
		}
	}

	return remote
}

// normalizeQuotaDomain maps a domain returned by Snowflake to the provider's spelling, keeping unknown domains as they are.
func normalizeQuotaDomain(domain string) sdk.QuotaSharedResourceDomain {
	if normalized, err := sdk.ToQuotaSharedResourceDomain(strings.ReplaceAll(domain, "_", " ")); err == nil {
		return normalized
	}
	return sdk.QuotaSharedResourceDomain(domain)
}

func preferCurrentQuotaValue[T any](current []T, remote T, key func(T) string) T {
	if idx := slices.IndexFunc(current, func(c T) bool { return key(c) == key(remote) }); idx >= 0 {
		return current[idx]
	}
	return remote
}

func (c *quotaConfiguration) sharedResourcesToSchema() []map[string]any {
	result := make([]map[string]any, len(c.sharedResources))
	for i, resource := range c.sharedResources {
		result[i] = map[string]any{"domain": string(resource.domain), "instance": resource.instance}
	}
	return result
}

func (c *quotaConfiguration) userTagsToSchema() []map[string]any {
	if c.userTagsOperator == nil && len(c.userTags) == 0 {
		return []map[string]any{}
	}
	operator := sdk.QuotaUserTagsOperatorUnion
	if c.userTagsOperator != nil {
		operator = *c.userTagsOperator
	}
	return []map[string]any{{"operator": string(operator), "tag": quotaTagValuesToSchema(c.userTags)}}
}

func quotaTagValuesToSchema(tags []quotaTagValue) []map[string]any {
	result := make([]map[string]any, len(tags))
	for i, tag := range tags {
		result[i] = map[string]any{"tag": tag.raw, "value": tag.value}
	}
	return result
}

func (c *quotaConfiguration) notificationThresholdsToSchema() []map[string]any {
	result := make([]map[string]any, len(c.notificationThresholds))
	for i, threshold := range c.notificationThresholds {
		result[i] = map[string]any{
			"threshold":      threshold.threshold,
			"spend_strategy": string(threshold.spendStrategy),
			"notify_user":    threshold.notifyUser,
			"cycle":          string(threshold.cycle),
		}
	}
	return result
}

func (c *quotaConfiguration) notificationIntegrationsToSchema() []string {
	result := make([]string, len(c.notificationIntegrations))
	for i, integration := range c.notificationIntegrations {
		result[i] = integration.raw
	}
	return result
}

func (c *quotaConfiguration) customActionsToSchema() []map[string]any {
	result := make([]map[string]any, len(c.customActions))
	for i, action := range c.customActions {
		result[i] = map[string]any{
			"procedure":      action.raw,
			"arguments":      action.arguments,
			"spend_strategy": string(action.spendStrategy),
			"threshold":      action.threshold,
		}
	}
	return result
}

func (c *quotaConfiguration) cycleStartActionToSchema() []map[string]any {
	if c.cycleStartAction == nil {
		return []map[string]any{}
	}
	return []map[string]any{{"procedure": c.cycleStartAction.raw, "arguments": c.cycleStartAction.arguments}}
}

// quotaDiffByKey returns the elements of previous missing from desired and the elements of desired missing from previous.
func quotaDiffByKey[T any](previous []T, desired []T, key func(T) string) (removed []T, added []T) {
	previousKeys := make(map[string]struct{}, len(previous))
	for _, element := range previous {
		previousKeys[key(element)] = struct{}{}
	}
	desiredKeys := make(map[string]struct{}, len(desired))
	for _, element := range desired {
		desiredKeys[key(element)] = struct{}{}
	}
	for _, element := range previous {
		if _, ok := desiredKeys[key(element)]; !ok {
			removed = append(removed, element)
		}
	}
	for _, element := range desired {
		if _, ok := previousKeys[key(element)]; !ok {
			added = append(added, element)
		}
	}
	return removed, added
}

func quotaKeysEqual[T any](a []T, b []T, key func(T) string) bool {
	removed, added := quotaDiffByKey(a, b, key)
	return len(removed) == 0 && len(added) == 0
}
