package sdk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// QuotaDetails aggregates the configuration of a quota, read with its GET_* instance methods.
// Quotas have no DESCRIBE command, so this is what DescribeDetails returns.
type QuotaDetails struct {
	Id                       SchemaObjectIdentifier
	Scope                    QuotaScope
	Config                   QuotaConfig
	NotificationThresholds   []QuotaNotificationThreshold
	NotificationIntegrations []QuotaNotificationIntegration
	CustomActions            []QuotaCustomAction
	CycleStartAction         *QuotaCycleStartAction
}

// QuotaScope is the result of GET_QUOTA_SCOPE, following the JSON example in the per-user quotas guide.
// TODO [quota spike]: verify the JSON shape, the user_id type, and the tag "type" values (included vs. excluded tags).
type QuotaScope struct {
	ExcludedUsers   []QuotaScopeUser           `json:"excluded_users"`
	IncludedUsers   []QuotaScopeUser           `json:"included_users"`
	SharedResources []QuotaScopeSharedResource `json:"shared_resources"`
	UserTags        *QuotaScopeUserTags        `json:"user_tags"`
}

// Numeric values are kept as json.Number, which accepts both JSON numbers and numeric strings.
type QuotaScopeUser struct {
	UserId   json.Number `json:"user_id"`
	UserName string      `json:"user_name"`
}

type QuotaScopeSharedResource struct {
	Domain string      `json:"domain"`
	Id     json.Number `json:"id"`
	Name   string      `json:"name"`
}

// IsWholeDomain reports whether the shared resource covers the whole domain rather than a single instance (id -1).
func (r QuotaScopeSharedResource) IsWholeDomain() bool {
	id, err := r.Id.Int64()
	return err == nil && id == -1
}

// NormalizedDomain maps the domain as returned by GET_QUOTA_SCOPE (the guide's example uses CORTEX_AGENT, with an underscore)
// to the domain accepted by ADD_SHARED_RESOURCE (CORTEX AGENT).
func (r QuotaScopeSharedResource) NormalizedDomain() (QuotaSharedResourceDomain, error) {
	return ToQuotaSharedResourceDomain(strings.ReplaceAll(r.Domain, "_", " "))
}

type QuotaScopeUserTags struct {
	Operator string          `json:"operator"`
	Tags     []QuotaScopeTag `json:"tags"`
}

type QuotaScopeTag struct {
	Type        string   `json:"type"`
	TagDatabase string   `json:"tag_database"`
	TagSchema   string   `json:"tag_schema"`
	TagKey      string   `json:"tag_key"`
	TagValues   []string `json:"tag_values"`
}

func (t QuotaScopeTag) TagId() SchemaObjectIdentifier {
	return NewSchemaObjectIdentifier(t.TagDatabase, t.TagSchema, t.TagKey)
}

// QuotaConfig is the result of GET_CONFIG. The guide names only the keys below ("including", so there may be more).
// TODO [quota spike]: verify the JSON shape, and whether the block enforcement email flag and admin emails are returned.
type QuotaConfig struct {
	PerUserLimit            *json.Number `json:"per_user_limit"`
	PerUserLimitWeekly      *json.Number `json:"per_user_limit_weekly"`
	PerUserLimitDaily       *json.Number `json:"per_user_limit_daily"`
	BlockEnforcementEnabled *bool        `json:"block_enforcement_enabled"`
}

// PerUserLimitFor returns the limit of the given cycle, or nil when it isn't set.
func (c QuotaConfig) PerUserLimitFor(cycle QuotaCycle) (*float64, error) {
	var limit *json.Number
	switch cycle {
	case QuotaCycleMonthly:
		limit = c.PerUserLimit
	case QuotaCycleWeekly:
		limit = c.PerUserLimitWeekly
	case QuotaCycleDaily:
		limit = c.PerUserLimitDaily
	default:
		return nil, fmt.Errorf("unsupported quota cycle: %s", cycle)
	}
	if limit == nil || *limit == "" {
		return nil, nil
	}
	value, err := limit.Float64()
	if err != nil {
		return nil, fmt.Errorf("failed to parse the %s per-user limit %q: %w", cycle, *limit, err)
	}
	return &value, nil
}

// ProcedureId parses the PROCEDURE_FQN returned by GET_CUSTOM_ACTIONS.
// TODO [quota spike]: verify the PROCEDURE_FQN format (quoting, argument types).
func (a QuotaCustomAction) ProcedureId() (SchemaObjectIdentifierWithArguments, error) {
	return ParseSchemaObjectIdentifierWithArguments(a.ProcedureFqn)
}

// IsSet reports whether a cycle-start action is configured (GET_CYCLE_START_ACTION may return a row of NULLs otherwise).
func (a QuotaCycleStartAction) IsSet() bool {
	return a.ProcedureFqn != nil && *a.ProcedureFqn != ""
}

// ProcedureId parses the PROCEDURE_FQN returned by GET_CYCLE_START_ACTION.
func (a QuotaCycleStartAction) ProcedureId() (SchemaObjectIdentifierWithArguments, error) {
	if !a.IsSet() {
		return SchemaObjectIdentifierWithArguments{}, errors.New("no cycle-start action is set")
	}
	return ParseSchemaObjectIdentifierWithArguments(*a.ProcedureFqn)
}

// ParseQuotaProcedureArguments parses the PROCEDURE_ARGS column of GET_CUSTOM_ACTIONS and GET_CYCLE_START_ACTION.
// The column is an ARRAY, which the driver returns as JSON (e.g. ["a", "b"]); the comma-separated form is accepted as a fallback,
// and an empty (or NULL) value yields no arguments.
// TODO [quota spike]: confirm the ARRAY representation against a live account.
func ParseQuotaProcedureArguments(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{}, nil
	}
	var arguments []string
	if err := json.Unmarshal([]byte(trimmed), &arguments); err == nil {
		if arguments == nil {
			arguments = []string{}
		}
		return arguments, nil
	}
	return ParseCommaSeparatedStringArray(trimmed, true), nil
}

// ParseQuotaScope parses the output of GET_QUOTA_SCOPE. JSON keys are matched case-insensitively.
func ParseQuotaScope(value string) (*QuotaScope, error) {
	scope := new(QuotaScope)
	if err := json.Unmarshal([]byte(value), scope); err != nil {
		return nil, fmt.Errorf("failed to parse quota scope %q: %w", value, err)
	}
	return scope, nil
}

// ParseQuotaConfig parses the output of GET_CONFIG. JSON keys are matched case-insensitively.
func ParseQuotaConfig(value string) (*QuotaConfig, error) {
	config := new(QuotaConfig)
	if err := json.Unmarshal([]byte(value), config); err != nil {
		return nil, fmt.Errorf("failed to parse quota config %q: %w", value, err)
	}
	return config, nil
}

func (v *quotas) DescribeDetails(ctx context.Context, id SchemaObjectIdentifier) (*QuotaDetails, error) {
	details := &QuotaDetails{Id: id}

	scope, err := v.GetQuotaScope(ctx, NewGetQuotaScopeQuotaRequest(id))
	if err != nil {
		return nil, err
	}
	parsedScope, err := ParseQuotaScope(*scope)
	if err != nil {
		return nil, err
	}
	details.Scope = *parsedScope

	config, err := v.GetConfig(ctx, NewGetConfigQuotaRequest(id))
	if err != nil {
		return nil, err
	}
	parsedConfig, err := ParseQuotaConfig(*config)
	if err != nil {
		return nil, err
	}
	details.Config = *parsedConfig

	if details.NotificationThresholds, err = v.GetNotificationThresholds(ctx, NewGetNotificationThresholdsQuotaRequest(id)); err != nil {
		return nil, err
	}
	if details.NotificationIntegrations, err = v.GetNotificationIntegrations(ctx, NewGetNotificationIntegrationsQuotaRequest(id)); err != nil {
		return nil, err
	}
	if details.CustomActions, err = v.GetCustomActions(ctx, NewGetCustomActionsQuotaRequest(id)); err != nil {
		return nil, err
	}

	cycleStartAction, err := v.GetCycleStartAction(ctx, NewGetCycleStartActionQuotaRequest(id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case cycleStartAction.IsSet():
		details.CycleStartAction = cycleStartAction
	}

	return details, nil
}

// quotaDomainsWithPlainInstances are the domains whose instances are passed by name (e.g. AI_CLASSIFY or CORTEX_CODE_CLI);
// instances of the other domains are passed as object references.
var quotaDomainsWithPlainInstances = []QuotaSharedResourceDomain{
	QuotaSharedResourceDomainAiFunction,
	QuotaSharedResourceDomainCortexCode,
}

func validateQuotaSharedResourceInstance(domain QuotaSharedResourceDomain, instanceName *string, warehouse *AccountObjectIdentifier, cortexAgent *SchemaObjectIdentifier, snowflakeIntelligence *AccountObjectIdentifier, aiGateway *AccountObjectIdentifier) error {
	var errs []error
	if _, err := ToQuotaSharedResourceDomain(string(domain)); err != nil {
		errs = append(errs, err)
	}
	if instanceName != nil && !slices.Contains(quotaDomainsWithPlainInstances, domain) {
		plainDomains := make([]string, len(quotaDomainsWithPlainInstances))
		for i, plainDomain := range quotaDomainsWithPlainInstances {
			plainDomains[i] = string(plainDomain)
		}
		errs = append(errs, fmt.Errorf("InstanceName can only be set for the %s domains, got %s", strings.Join(plainDomains, ", "), domain))
	}
	referenceFields := []struct {
		name   string
		isSet  bool
		domain QuotaSharedResourceDomain
	}{
		{"Warehouse", warehouse != nil, QuotaSharedResourceDomainWarehouse},
		{"CortexAgent", cortexAgent != nil, QuotaSharedResourceDomainCortexAgent},
		{"SnowflakeIntelligence", snowflakeIntelligence != nil, QuotaSharedResourceDomainSnowflakeIntelligence},
		{"AiGateway", aiGateway != nil, QuotaSharedResourceDomainAiGateway},
	}
	for _, field := range referenceFields {
		if field.isSet && field.domain != domain {
			errs = append(errs, fmt.Errorf("%s can only be set for the %s domain, got %s", field.name, field.domain, domain))
		}
	}
	return errors.Join(errs...)
}

func (v *QuotaAddSharedResourceArgs) additionalValidations() error {
	return validateQuotaSharedResourceInstance(v.Domain, v.InstanceName, v.Warehouse, v.CortexAgent, v.SnowflakeIntelligence, v.AiGateway)
}

func (v *QuotaRemoveSharedResourceArgs) additionalValidations() error {
	return validateQuotaSharedResourceInstance(v.Domain, v.InstanceName, v.Warehouse, v.CortexAgent, v.SnowflakeIntelligence, v.AiGateway)
}

func (v *QuotaRemoveCustomActionsArgs) additionalValidations() error {
	// the arguments are positional, so the procedure can only be passed together with the threshold
	if v.ProcedureFqn != nil && v.Threshold == nil {
		return errors.New("ProcedureFqn can only be set together with Threshold")
	}
	return nil
}

// quotaSharedResourceInstance is a domain instance parsed from its string representation.
type quotaSharedResourceInstance struct {
	name                  *string
	warehouse             *AccountObjectIdentifier
	cortexAgent           *SchemaObjectIdentifier
	snowflakeIntelligence *AccountObjectIdentifier
	aiGateway             *AccountObjectIdentifier
}

// parseQuotaSharedResourceInstance parses an instance of the given domain: a plain name for AI FUNCTION and CORTEX CODE,
// a fully qualified name for CORTEX AGENT, and an account-level object name for the other domains.
// An empty instance means the whole domain.
func parseQuotaSharedResourceInstance(domain QuotaSharedResourceDomain, instance string) (quotaSharedResourceInstance, error) {
	var result quotaSharedResourceInstance
	if instance == "" {
		return result, nil
	}
	switch domain {
	case QuotaSharedResourceDomainAiFunction, QuotaSharedResourceDomainCortexCode:
		result.name = &instance
	case QuotaSharedResourceDomainCortexAgent:
		id, err := ParseSchemaObjectIdentifier(instance)
		if err != nil {
			return result, err
		}
		result.cortexAgent = &id
	case QuotaSharedResourceDomainWarehouse, QuotaSharedResourceDomainSnowflakeIntelligence, QuotaSharedResourceDomainAiGateway:
		id, err := ParseAccountObjectIdentifier(instance)
		if err != nil {
			return result, err
		}
		switch domain {
		case QuotaSharedResourceDomainWarehouse:
			result.warehouse = &id
		case QuotaSharedResourceDomainSnowflakeIntelligence:
			result.snowflakeIntelligence = &id
		default:
			result.aiGateway = &id
		}
	default:
		return result, fmt.Errorf("unsupported quota shared resource domain: %s", domain)
	}
	return result, nil
}

// NewQuotaAddSharedResourceArgsRequestFromInstance builds ADD_SHARED_RESOURCE arguments from a domain and the string
// representation of its instance (see parseQuotaSharedResourceInstance); an empty instance adds the whole domain.
func NewQuotaAddSharedResourceArgsRequestFromInstance(domain QuotaSharedResourceDomain, instance string) (*QuotaAddSharedResourceArgsRequest, error) {
	parsed, err := parseQuotaSharedResourceInstance(domain, instance)
	if err != nil {
		return nil, err
	}
	request := NewQuotaAddSharedResourceArgsRequest(domain)
	request.InstanceName = parsed.name
	request.Warehouse = parsed.warehouse
	request.CortexAgent = parsed.cortexAgent
	request.SnowflakeIntelligence = parsed.snowflakeIntelligence
	request.AiGateway = parsed.aiGateway
	return request, nil
}

// NewQuotaRemoveSharedResourceArgsRequestFromInstance is the REMOVE_SHARED_RESOURCE counterpart of
// NewQuotaAddSharedResourceArgsRequestFromInstance.
func NewQuotaRemoveSharedResourceArgsRequestFromInstance(domain QuotaSharedResourceDomain, instance string) (*QuotaRemoveSharedResourceArgsRequest, error) {
	parsed, err := parseQuotaSharedResourceInstance(domain, instance)
	if err != nil {
		return nil, err
	}
	request := NewQuotaRemoveSharedResourceArgsRequest(domain)
	request.InstanceName = parsed.name
	request.Warehouse = parsed.warehouse
	request.CortexAgent = parsed.cortexAgent
	request.SnowflakeIntelligence = parsed.snowflakeIntelligence
	request.AiGateway = parsed.aiGateway
	return request, nil
}

// NewQuotaSetAdminEmailsArgsRequestFromEmails joins the emails into the single comma-separated string SET_ADMIN_EMAILS expects.
func NewQuotaSetAdminEmailsArgsRequestFromEmails(emails ...string) *QuotaSetAdminEmailsArgsRequest {
	return NewQuotaSetAdminEmailsArgsRequest(strings.Join(emails, ", "))
}
