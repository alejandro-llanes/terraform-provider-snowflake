package defs

import (
	g "github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk/generator/gen"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk/generator/gen/sdkcommons"
)

// Per-user quotas are documented only in the user guide (https://docs.snowflake.com/en/user-guide/budgets/per-user-quotas);
// there is no class reference yet. The syntax below follows the guide's examples. Where the guide is silent, it mirrors
// the BUDGET class and is marked with "TODO [quota spike]" until verified against a live account.

const quotasDocs = "https://docs.snowflake.com/en/user-guide/budgets/per-user-quotas"

var QuotaSharedResourceDomainEnumDef = g.NewEnum(
	"QuotaSharedResourceDomain", "QuotaSharedResourceDomains",
	"WAREHOUSE", "AI FUNCTION", "CORTEX AGENT", "CORTEX CODE", "SNOWFLAKE INTELLIGENCE", "AI GATEWAY",
)

var QuotaCycleEnumDef = g.NewEnum(
	"QuotaCycle", "QuotaCycles",
	"MONTHLY", "WEEKLY", "DAILY",
)

var QuotaSpendStrategyEnumDef = g.NewEnum(
	"QuotaSpendStrategy", "QuotaSpendStrategies",
	"ACTUAL", "PROJECTED",
)

var QuotaUserTagsOperatorEnumDef = g.NewEnum(
	"QuotaUserTagsOperator", "QuotaUserTagsOperators",
	"UNION", "INTERSECTION",
)

// quotaSharedResourceArgs renders ('<domain>'[, '<instance>' | (SELECT SYSTEM$REFERENCE('<type>', '<name>'))]).
// AI FUNCTION and CORTEX CODE instances are plain names; the other domains take object references.
// None of the instance fields set means the whole domain.
func quotaSharedResourceArgs(name string) *g.QueryStruct {
	reference := func(objectType string) *g.IdentifierTransformer {
		return g.IdentifierOptions().SystemReference(objectType).InSubquery()
	}
	return g.NewQueryStruct(name).
		Enum("Domain", QuotaSharedResourceDomainEnumDef, g.KeywordOptions().SingleQuotes().Required()).
		OptionalText("InstanceName", g.KeywordOptions().SingleQuotes()).
		OptionalIdentifier("Warehouse", g.KindOfT[sdkcommons.AccountObjectIdentifier](), reference("WAREHOUSE")).
		OptionalIdentifier("CortexAgent", g.KindOfT[sdkcommons.SchemaObjectIdentifier](), reference("CORTEX AGENT")).
		// TODO [quota spike]: verify the identifier kind and reference type of Snowflake Intelligence and AI Gateway objects.
		OptionalIdentifier("SnowflakeIntelligence", g.KindOfT[sdkcommons.AccountObjectIdentifier](), reference("SNOWFLAKE INTELLIGENCE")).
		OptionalIdentifier("AiGateway", g.KindOfT[sdkcommons.AccountObjectIdentifier](), reference("AI GATEWAY")).
		WithValidation(g.MoreThanOneValueSet, "InstanceName", "Warehouse", "CortexAgent", "SnowflakeIntelligence", "AiGateway").
		WithAdditionalValidations()
}

// quotaTagValue renders [(SELECT SYSTEM$REFERENCE('TAG', '<tag>', 'SESSION', 'APPLYBUDGET')), '<value>'].
func quotaTagValue(name string) *g.QueryStruct {
	return g.NewQueryStruct(name).
		Identifier("Tag", g.KindOfT[sdkcommons.SchemaObjectIdentifier](), g.IdentifierOptions().Required().SystemReference("TAG", "SESSION", "APPLYBUDGET").InSubquery()).
		Text("Value", g.KeywordOptions().SingleQuotes().Required()).
		WithValidation(g.ValidIdentifier, "Tag")
}

// SET_USER_TAGS overwrites the user tags; the guide's signature shows the operator as required (UNION is the documented default).
var quotaSetUserTagsArgs = g.NewQueryStruct("SetUserTagsArgs").
	ListQueryStructField("Tags", quotaTagValue("QuotaUserTag"), g.ListOptions().Required().MustBrackets().ArrayElements()).
	OptionalEnum("Operator", QuotaUserTagsOperatorEnumDef, g.KeywordOptions().SingleQuotes())

// EXCLUDE_USERS has two forms: ('TAG', [[<tag>, '<value>'], ...]) and ('USER', ['<user>', ...]).
// Like INCLUDE_USERS, calls accumulate; an empty array clears the list.
var quotaExcludeUsersArgs = g.NewQueryStruct("ExcludeUsersArgs").
	OptionalQueryStructField(
		"ByTag",
		g.NewQueryStruct("QuotaExcludeUsersByTag").
			SQLWithCustomFieldName("kind", "'TAG'").
			ListQueryStructField("Tags", quotaTagValue("QuotaExcludedUserTag"), g.ListOptions().Required().MustBrackets().ArrayElements()),
		g.ListOptions(),
	).
	OptionalQueryStructField(
		"ByUser",
		g.NewQueryStruct("QuotaExcludeUsersByUser").
			SQLWithCustomFieldName("kind", "'USER'").
			List("Users", "string", g.ListOptions().Required().MustBrackets().SingleQuotes()),
		g.ListOptions(),
	).
	WithValidation(g.ExactlyOneValueSet, "ByTag", "ByUser")

// INCLUDE_USERS calls accumulate; an empty array clears the list.
var quotaIncludeUsersArgs = g.NewQueryStruct("IncludeUsersArgs").
	List("Users", "string", g.ListOptions().Required().MustBrackets().SingleQuotes())

// TODO [quota spike]: verify whether limits accept decimal credit values.
var quotaSetPerUserLimitArgs = g.NewQueryStruct("SetPerUserLimitArgs").
	PredefinedQueryStructField("Limit", "int", g.ParameterOptions().NoEquals().Required()).
	OptionalEnum("Cycle", QuotaCycleEnumDef, g.KeywordOptions().SingleQuotes())

// TODO [quota spike]: the guide only shows UNSET_PER_USER_LIMIT('WEEKLY'); verify how the monthly limit is unset.
var quotaUnsetPerUserLimitArgs = g.NewQueryStruct("UnsetPerUserLimitArgs").
	Enum("Cycle", QuotaCycleEnumDef, g.KeywordOptions().SingleQuotes().Required())

var quotaSetBlockEnforcementEnabledArgs = g.NewQueryStruct("SetBlockEnforcementEnabledArgs").
	PredefinedQueryStructField("Enabled", "bool", g.ParameterOptions().NoEquals().Required()).
	PredefinedQueryStructField("NotifyUsers", "*bool", g.ParameterOptions().NoEquals())

var quotaAddNotificationThresholdArgs = g.NewQueryStruct("AddNotificationThresholdArgs").
	PredefinedQueryStructField("Threshold", "int", g.ParameterOptions().NoEquals().Required()).
	Enum("SpendStrategy", QuotaSpendStrategyEnumDef, g.KeywordOptions().SingleQuotes().Required()).
	PredefinedQueryStructField("NotifyUser", "bool", g.ParameterOptions().NoEquals().Required()).
	Enum("Cycle", QuotaCycleEnumDef, g.KeywordOptions().SingleQuotes().Required())

var quotaRemoveNotificationThresholdArgs = g.NewQueryStruct("RemoveNotificationThresholdArgs").
	PredefinedQueryStructField("Threshold", "int", g.ParameterOptions().NoEquals().Required()).
	Enum("SpendStrategy", QuotaSpendStrategyEnumDef, g.KeywordOptions().SingleQuotes().Required())

func quotaNotificationIntegrationArgs(name string) *g.QueryStruct {
	return g.NewQueryStruct(name).
		Identifier("Integration", g.KindOfT[sdkcommons.AccountObjectIdentifier](), g.IdentifierOptions().SingleQuotes().Required()).
		WithValidation(g.ValidIdentifier, "Integration")
}

// SET_ADMIN_EMAILS takes a single string with comma-separated emails.
var quotaSetAdminEmailsArgs = g.NewQueryStruct("SetAdminEmailsArgs").
	Text("Emails", g.KeywordOptions().SingleQuotes().Required())

var quotaAddCustomActionArgs = g.NewQueryStruct("AddCustomActionArgs").
	Identifier("Procedure", g.KindOfT[sdkcommons.SchemaObjectIdentifierWithArguments](), g.IdentifierOptions().Required().SystemReference("PROCEDURE")).
	List("Arguments", "string", g.ListOptions().Required().MustBrackets().SingleQuotes()).
	Enum("SpendStrategy", QuotaSpendStrategyEnumDef, g.KeywordOptions().SingleQuotes().Required()).
	PredefinedQueryStructField("Threshold", "int", g.ParameterOptions().NoEquals().Required()).
	WithValidation(g.ValidIdentifier, "Procedure")

// REMOVE_CUSTOM_ACTIONS([<threshold>[, '<procedure fqn with argument types>']]); the arguments are positional.
// The procedure is passed as the PROCEDURE_FQN string returned by GET_CUSTOM_ACTIONS, so that it matches the registered action
// whatever its exact format is.
var quotaRemoveCustomActionsArgs = g.NewQueryStruct("RemoveCustomActionsArgs").
	OptionalNumber("Threshold", g.KeywordOptions()).
	OptionalText("ProcedureFqn", g.KeywordOptions().SingleQuotes()).
	WithAdditionalValidations()

var quotaSetCycleStartActionArgs = g.NewQueryStruct("SetCycleStartActionArgs").
	Identifier("Procedure", g.KindOfT[sdkcommons.SchemaObjectIdentifierWithArguments](), g.IdentifierOptions().Required().SystemReference("PROCEDURE")).
	List("Arguments", "string", g.ListOptions().Required().MustBrackets().SingleQuotes()).
	WithValidation(g.ValidIdentifier, "Procedure")

var quotaNotificationThresholdResult = g.StructPair("quotaNotificationThresholdRow", "QuotaNotificationThreshold").
	// uppercase column names used on purpose
	Number("THRESHOLD").
	Enum("SPEND_STRATEGY", QuotaSpendStrategyEnumDef).
	Bool("NOTIFY_USER").
	Time("ADDED_TIMESTAMP")

// TODO [quota spike]: the guide doesn't document the columns; these mirror the BUDGET class.
var quotaNotificationIntegrationResult = g.StructPair("quotaNotificationIntegrationRow", "QuotaNotificationIntegration").
	Text("integration_name").
	OptionalNumber("last_notification_time").
	OptionalTime("added_date")

// PROCEDURE_FQN is kept as returned (its exact format is not documented); QuotaCustomAction.ProcedureId() parses it.
var quotaCustomActionResult = g.StructPair("quotaCustomActionRow", "QuotaCustomAction").
	// uppercase column names used on purpose
	Text("ACTION_ID").
	Text("PROCEDURE_FQN").
	Field("PROCEDURE_ARGS", "string", "[]string", g.WithCustomParser("ParseQuotaProcedureArguments")).
	Enum("SPEND_STRATEGY", QuotaSpendStrategyEnumDef).
	Number("THRESHOLD").
	OptionalTime("LAST_TRIGGER_ATTEMPT_TIME").
	Time("ADDED_TIMESTAMP")

// The columns mirror the BUDGET class. Every column is nullable, so that a row of NULLs (a possible result when no action is set)
// can be scanned; QuotaCycleStartAction.IsSet() tells the two apart.
// TODO [quota spike]: verify the result when no cycle-start action is set (no rows vs. a row of NULLs).
var quotaCycleStartActionResult = g.StructPair("quotaCycleStartActionRow", "QuotaCycleStartAction").
	// uppercase column names used on purpose
	OptionalText("ACTION_UUID").
	OptionalText("PROCEDURE_FQN").
	Field("PROCEDURE_ARGS", "sql.NullString", "[]string", g.WithCustomParser("ParseQuotaProcedureArguments")).
	OptionalTime("ADDED_TIMESTAMP").
	OptionalTime("LAST_TRIGGERED_TIMESTAMP")

var quotasDef = g.NewInterface(
	"Quotas",
	"Quota",
	g.KindOfT[sdkcommons.SchemaObjectIdentifier](),
).CreateOperation(
	quotasDocs,
	// TODO [quota spike]: the guide only shows CREATE SNOWFLAKE.CORE.QUOTA <name>(); OR REPLACE, IF NOT EXISTS and COMMENT mirror the BUDGET class.
	g.NewQueryStruct("CreateQuotaOptions").
		Create().
		OrReplace().
		SQLWithCustomFieldName("snowflakeCoreQuota", "SNOWFLAKE.CORE.QUOTA").
		IfNotExists().
		Name().
		SQLWithCustomFieldName("parens", "()").
		OptionalComment().
		WithValidation(g.ValidIdentifier, "name").
		WithValidation(g.ConflictingFields, "OrReplace", "IfNotExists"),
).DropOperation(
	quotasDocs,
	// TODO [quota spike]: DROP is not documented for quotas; it mirrors DROP SNOWFLAKE.CORE.BUDGET.
	g.NewQueryStruct("DropQuotaOptions").
		Drop().
		SQLWithCustomFieldName("snowflakeCoreQuota", "SNOWFLAKE.CORE.QUOTA").
		IfExists().
		Name().
		WithValidation(g.ValidIdentifier, "name"),
).ShowOperationWithPairedStructs(
	quotasDocs,
	// TODO [quota spike]: SHOW is not documented for quotas; the columns and filters mirror SHOW SNOWFLAKE.CORE.BUDGET.
	g.StructPair("quotaDBRow", "Quota").
		Time("created_on").
		Text("name").
		Text("database_name").
		Text("schema_name").
		OptionalText("current_version").
		OptionalText("comment").
		Text("owner").
		OptionalText("owner_role_type"),
	g.NewQueryStruct("ShowQuotas").
		Show().
		SQLWithCustomFieldName("snowflakeCoreQuota", "SNOWFLAKE.CORE.QUOTA").
		OptionalLike().
		OptionalIn().
		OptionalLimit(),
	g.ShowByIDLikeFiltering,
	g.ShowByIDInFiltering,
).InstanceMethodOperationScalar(
	quotasDocs,
	"ADD_SHARED_RESOURCE",
	quotaSharedResourceArgs("AddSharedResourceArgs"),
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	// TODO [quota spike]: the guide only mentions REMOVE_SHARED_RESOURCE; its arguments mirror ADD_SHARED_RESOURCE and the BUDGET class.
	"REMOVE_SHARED_RESOURCE",
	quotaSharedResourceArgs("RemoveSharedResourceArgs"),
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"SET_USER_TAGS",
	quotaSetUserTagsArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"EXCLUDE_USERS",
	quotaExcludeUsersArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"INCLUDE_USERS",
	quotaIncludeUsersArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"GET_QUOTA_SCOPE",
	nil,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"SET_PER_USER_LIMIT",
	quotaSetPerUserLimitArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"UNSET_PER_USER_LIMIT",
	quotaUnsetPerUserLimitArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"SET_BLOCK_ENFORCEMENT_ENABLED",
	quotaSetBlockEnforcementEnabledArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	// TODO [quota spike]: verify that GET_CONFIG returns a single JSON value.
	"GET_CONFIG",
	nil,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"ADD_NOTIFICATION_THRESHOLD",
	quotaAddNotificationThresholdArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"REMOVE_NOTIFICATION_THRESHOLD",
	quotaRemoveNotificationThresholdArgs,
	"string",
).InstanceMethodOperation(
	quotasDocs,
	"GET_NOTIFICATION_THRESHOLDS",
	nil,
	quotaNotificationThresholdResult,
	g.InstanceMethodKindSlice,
).InstanceMethodOperationScalar(
	quotasDocs,
	"ADD_NOTIFICATION_INTEGRATION",
	quotaNotificationIntegrationArgs("AddNotificationIntegrationArgs"),
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"REMOVE_NOTIFICATION_INTEGRATION",
	quotaNotificationIntegrationArgs("RemoveNotificationIntegrationArgs"),
	"string",
).InstanceMethodOperation(
	quotasDocs,
	"GET_NOTIFICATION_INTEGRATIONS",
	nil,
	quotaNotificationIntegrationResult,
	g.InstanceMethodKindSlice,
).InstanceMethodOperationScalar(
	quotasDocs,
	// TODO [quota spike]: there is no documented getter for admin emails; check GET_CONFIG and how to clear them.
	"SET_ADMIN_EMAILS",
	quotaSetAdminEmailsArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"ADD_CUSTOM_ACTION",
	quotaAddCustomActionArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"REMOVE_CUSTOM_ACTIONS",
	quotaRemoveCustomActionsArgs,
	"string",
).InstanceMethodOperation(
	quotasDocs,
	"GET_CUSTOM_ACTIONS",
	nil,
	quotaCustomActionResult,
	g.InstanceMethodKindSlice,
).InstanceMethodOperationScalar(
	quotasDocs,
	"SET_CYCLE_START_ACTION",
	quotaSetCycleStartActionArgs,
	"string",
).InstanceMethodOperationScalar(
	quotasDocs,
	"REMOVE_CYCLE_START_ACTION",
	nil,
	"string",
).InstanceMethodOperation(
	quotasDocs,
	// TODO [quota spike]: verify the result when no cycle-start action is set (no rows vs. a row of NULLs).
	"GET_CYCLE_START_ACTION",
	nil,
	quotaCycleStartActionResult,
	g.InstanceMethodKindSingleValue,
).
	WithCustomInterfaceMethod("DescribeDetails", "", []*g.MethodParameter{g.NewMethodParameter("id", g.KindOfT[sdkcommons.SchemaObjectIdentifier]())}, "*QuotaDetails", "error").
	WithEnums(QuotaSharedResourceDomainEnumDef, QuotaCycleEnumDef, QuotaSpendStrategyEnumDef, QuotaUserTagsOperatorEnumDef)
