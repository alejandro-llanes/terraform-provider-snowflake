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

// QuotaScope is the result of GET_QUOTA_SCOPE, a JSON value. The shape below was verified against a live account; the user tags
// differ from the guide's example (camelCase keys, no "type").
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
	// CortexAgentId is the agent a CORTEX AGENT instance refers to. GET_QUOTA_SCOPE returns the agent's name only;
	// DescribeDetails resolves it when exactly one agent with that name exists in the account.
	CortexAgentId *SchemaObjectIdentifier `json:"-"`
}

// IsWholeDomain reports whether the shared resource covers the whole domain rather than a single instance (id -1).
func (r QuotaScopeSharedResource) IsWholeDomain() bool {
	id, err := r.Id.Int64()
	return err == nil && id == -1
}

// NormalizedDomain maps the domain as returned by GET_QUOTA_SCOPE (e.g. CORTEX_AGENT, with an underscore)
// to the domain accepted by ADD_SHARED_RESOURCE (CORTEX AGENT).
func (r QuotaScopeSharedResource) NormalizedDomain() (QuotaSharedResourceDomain, error) {
	return ToQuotaSharedResourceDomain(strings.ReplaceAll(r.Domain, "_", " "))
}

// QuotaScopeUserTagsOperatorAllUsers is the operator of a quota whose users are not selected by tags. Its tags, if any,
// are the ones excluded with EXCLUDE_USERS('TAG', ...).
const QuotaScopeUserTagsOperatorAllUsers = "ALL_USERS"

type QuotaScopeUserTags struct {
	Operator string          `json:"operator"`
	Tags     []QuotaScopeTag `json:"tags"`
}

// AreExclusions reports whether the tags exclude users (operator ALL_USERS) rather than select them (UNION or INTERSECTION).
func (t QuotaScopeUserTags) AreExclusions() bool {
	return strings.EqualFold(t.Operator, QuotaScopeUserTagsOperatorAllUsers)
}

type QuotaScopeTag struct {
	TagId_      json.Number `json:"tagId"`
	TagDatabase string      `json:"tagDatabase"`
	TagSchema   string      `json:"tagSchema"`
	TagName     string      `json:"tagName"`
	TagValues   []string    `json:"tagValues"`
}

func (t QuotaScopeTag) TagId() SchemaObjectIdentifier {
	return NewSchemaObjectIdentifier(t.TagDatabase, t.TagSchema, t.TagName)
}

// PerUserLimitFor returns the limit of the given cycle, or nil when it isn't set.
func (c QuotaConfig) PerUserLimitFor(cycle QuotaCycle) (*int, error) {
	switch cycle {
	case QuotaCycleMonthly:
		return c.PerUserLimit, nil
	case QuotaCycleWeekly:
		return c.PerUserLimitWeekly, nil
	case QuotaCycleDaily:
		return c.PerUserLimitDaily, nil
	default:
		return nil, fmt.Errorf("unsupported quota cycle: %s", cycle)
	}
}

// ParseQuotaAdminEmails parses the ADMIN_EMAILS column of GET_CONFIG.
// TODO [quota spike]: SET_ADMIN_EMAILS only accepts verified emails, so the column couldn't be verified with a value set;
// the comma-separated form (as passed to SET_ADMIN_EMAILS) and a JSON array are both accepted.
func ParseQuotaAdminEmails(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{}, nil
	}
	var emails []string
	if err := json.Unmarshal([]byte(trimmed), &emails); err == nil {
		return emails, nil
	}
	return ParseCommaSeparatedStringArray(trimmed, true), nil
}

// ProcedureId parses the PROCEDURE_FQN returned by GET_CUSTOM_ACTIONS (unquoted, e.g. DB.SCH.PROC(VARCHAR)).
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
	if err := v.resolveCortexAgents(ctx, details.Scope.SharedResources); err != nil {
		return nil, err
	}

	config, err := v.GetConfig(ctx, NewGetConfigQuotaRequest(id))
	if err != nil {
		return nil, err
	}
	details.Config = *config

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

// resolveCortexAgents sets CortexAgentId of the CORTEX AGENT instances whose name matches exactly one agent in the account.
func (v *quotas) resolveCortexAgents(ctx context.Context, resources []QuotaScopeSharedResource) error {
	for i := range resources {
		resource := &resources[i]
		domain, err := resource.NormalizedDomain()
		if err != nil || domain != QuotaSharedResourceDomainCortexAgent || resource.IsWholeDomain() {
			continue
		}
		agents, err := v.client.CortexAgents.Show(ctx, NewShowCortexAgentRequest().
			WithLike(Like{Pattern: String(resource.Name)}).
			WithIn(ExtendedIn{In: In{Account: Bool(true)}}))
		if err != nil {
			return fmt.Errorf("resolving the Cortex Agent %s of the quota: %w", resource.Name, err)
		}
		agents = slices.DeleteFunc(agents, func(agent CortexAgent) bool { return agent.Name != resource.Name })
		if len(agents) == 1 {
			resource.CortexAgentId = new(agents[0].ID())
		}
	}
	return nil
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
