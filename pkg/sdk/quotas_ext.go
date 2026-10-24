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

type QuotaScopeUser struct {
	UserId   int64  `json:"user_id"`
	UserName string `json:"user_name"`
}

type QuotaScopeSharedResource struct {
	Domain string `json:"domain"`
	Id     int64  `json:"id"`
	Name   string `json:"name"`
}

// IsWholeDomain reports whether the shared resource covers the whole domain rather than a single instance.
func (r QuotaScopeSharedResource) IsWholeDomain() bool {
	return r.Id == -1
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

// QuotaConfig is the result of GET_CONFIG. The guide names only the keys below; limits are parsed as floats
// in case Snowflake returns them with a fractional part.
// TODO [quota spike]: verify the JSON shape, and whether the block enforcement email flag and admin emails are returned.
type QuotaConfig struct {
	PerUserLimit            *float64 `json:"per_user_limit"`
	PerUserLimitWeekly      *float64 `json:"per_user_limit_weekly"`
	PerUserLimitDaily       *float64 `json:"per_user_limit_daily"`
	BlockEnforcementEnabled *bool    `json:"block_enforcement_enabled"`
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
	case cycleStartAction.ProcedureId != nil:
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
	if instanceName != nil && !slices.Contains(quotaDomainsWithPlainInstances, domain) {
		errs = append(errs, fmt.Errorf("InstanceName can only be set for the %v domains, got %s", quotaDomainsWithPlainInstances, domain))
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
	if v.Procedure != nil && v.Threshold == nil {
		return errors.New("Procedure can only be set together with Threshold")
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
