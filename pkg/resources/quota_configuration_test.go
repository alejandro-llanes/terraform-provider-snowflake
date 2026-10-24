package resources

import (
	"testing"

	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_quotaConfigurationFromRaw(t *testing.T) {
	d := schema.TestResourceDataRaw(t, quotaSchema, map[string]any{
		"database": "DB",
		"schema":   "SCH",
		"name":     "QUOTA",
		"shared_resource": []any{
			map[string]any{"domain": "AI FUNCTION", "instance": "AI_CLASSIFY"},
			map[string]any{"domain": "CORTEX AGENT", "instance": "DB.SCH.AGENT"},
		},
		"user_tags": []any{map[string]any{
			"operator": "INTERSECTION",
			"tag":      []any{map[string]any{"tag": "DB.SCH.COST_CENTER", "value": "finance"}},
		}},
		"included_users":         []any{"ALICE"},
		"per_user_limit_monthly": 1000,
		"notification_threshold": []any{map[string]any{"threshold": 80, "spend_strategy": "ACTUAL", "notify_user": false, "cycle": "DAILY"}},
		"custom_action":          []any{map[string]any{"procedure": `"DB"."SCH"."PROC"(VARCHAR)`, "arguments": []any{"a"}, "spend_strategy": "ACTUAL", "threshold": 100}},
		"cycle_start_action":     []any{map[string]any{"procedure": `"DB"."SCH"."RESET"()`}},
	})

	config, err := quotaConfigurationFromRaw(d.Get)
	require.NoError(t, err)

	assert.ElementsMatch(t, []quotaSharedResource{
		{domain: sdk.QuotaSharedResourceDomainAiFunction, instance: "AI_CLASSIFY"},
		{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: "DB.SCH.AGENT"},
	}, config.sharedResources)
	require.NotNil(t, config.userTagsOperator)
	assert.Equal(t, sdk.QuotaUserTagsOperatorIntersection, *config.userTagsOperator)
	assert.Equal(t, []quotaTagValue{{tag: sdk.NewSchemaObjectIdentifier("DB", "SCH", "COST_CENTER"), raw: "DB.SCH.COST_CENTER", value: "finance"}}, config.userTags)
	assert.Empty(t, config.excludedUserTags)
	assert.Equal(t, []string{"ALICE"}, config.includedUsers)
	assert.Equal(t, 1000, config.perUserLimit(sdk.QuotaCycleMonthly))
	assert.Equal(t, IntDefault, config.perUserLimit(sdk.QuotaCycleWeekly))
	assert.Equal(t, []quotaNotificationThreshold{{threshold: 80, spendStrategy: sdk.QuotaSpendStrategyActual, notifyUser: false, cycle: sdk.QuotaCycleDaily}}, config.notificationThresholds)
	require.Len(t, config.customActions, 1)
	assert.Equal(t, sdk.NewSchemaObjectIdentifierWithArguments("DB", "SCH", "PROC", sdk.DataTypeVARCHAR), config.customActions[0].procedure)
	assert.Equal(t, []string{"a"}, config.customActions[0].arguments)
	require.NotNil(t, config.cycleStartAction)
	assert.Empty(t, config.cycleStartAction.arguments)
}

func Test_quotaConfigurationFromDetails(t *testing.T) {
	current := &quotaConfiguration{
		sharedResources:    []quotaSharedResource{{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH"."AGENT"`}},
		includedUsers:      []string{`"alice"`},
		notifyBlockedUsers: true,
		adminEmails:        []string{"admin@example.com"},
		notificationThresholds: []quotaNotificationThreshold{
			{threshold: 80, spendStrategy: sdk.QuotaSpendStrategyProjected, notifyUser: true, cycle: sdk.QuotaCycleWeekly},
		},
	}
	details := &sdk.QuotaDetails{
		Scope: sdk.QuotaScope{
			SharedResources: []sdk.QuotaScopeSharedResource{
				{Domain: "CORTEX AGENT", Id: 42, Name: "DB.SCH.AGENT"},
				{Domain: "AI_FUNCTION", Id: -1, Name: "AI FUNCTION"},
			},
			IncludedUsers: []sdk.QuotaScopeUser{{UserId: 1, UserName: "alice"}, {UserId: 2, UserName: "BOB"}},
			UserTags: &sdk.QuotaScopeUserTags{
				Operator: "UNION",
				Tags: []sdk.QuotaScopeTag{
					{Type: "INCLUDE", TagDatabase: "DB", TagSchema: "SCH", TagKey: "TAG", TagValues: []string{"a", "b"}},
					{Type: "EXCLUDE", TagDatabase: "DB", TagSchema: "SCH", TagKey: "TAG", TagValues: []string{"c"}},
				},
			},
		},
		Config: sdk.QuotaConfig{PerUserLimit: new(1000.0), BlockEnforcementEnabled: new(true)},
		NotificationThresholds: []sdk.QuotaNotificationThreshold{
			{Threshold: 80, SpendStrategy: sdk.QuotaSpendStrategyProjected, NotifyUser: true},
			{Threshold: 100, SpendStrategy: sdk.QuotaSpendStrategyActual},
		},
	}

	remote := quotaConfigurationFromDetails(details, current)

	// equivalent values keep the configured spelling
	assert.Equal(t, []quotaSharedResource{
		{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH"."AGENT"`},
		{domain: sdk.QuotaSharedResourceDomainAiFunction},
	}, remote.sharedResources)
	assert.Equal(t, []string{`"alice"`, "BOB"}, remote.includedUsers)

	// included and excluded tags are split, one element per value
	require.NotNil(t, remote.userTagsOperator)
	assert.Equal(t, sdk.QuotaUserTagsOperatorUnion, *remote.userTagsOperator)
	assert.Len(t, remote.userTags, 2)
	require.Len(t, remote.excludedUserTags, 1)
	assert.Equal(t, "c", remote.excludedUserTags[0].value)

	assert.Equal(t, 1000, remote.perUserLimit(sdk.QuotaCycleMonthly))
	assert.Equal(t, IntDefault, remote.perUserLimit(sdk.QuotaCycleDaily))
	assert.True(t, remote.blockEnforcementEnabled)

	// values that can't be read back come from the current configuration
	assert.True(t, remote.notifyBlockedUsers)
	assert.Equal(t, []string{"admin@example.com"}, remote.adminEmails)
	assert.Equal(t, []quotaNotificationThreshold{
		{threshold: 80, spendStrategy: sdk.QuotaSpendStrategyProjected, notifyUser: true, cycle: sdk.QuotaCycleWeekly},
		{threshold: 100, spendStrategy: sdk.QuotaSpendStrategyActual, cycle: sdk.QuotaCycleMonthly},
	}, remote.notificationThresholds)
	assert.Nil(t, remote.cycleStartAction)
}

func Test_quotaSharedResource_key(t *testing.T) {
	testCases := []struct {
		name string
		a, b quotaSharedResource
	}{
		{"plain instance names are case-insensitive", quotaSharedResource{sdk.QuotaSharedResourceDomainAiFunction, "ai_classify"}, quotaSharedResource{sdk.QuotaSharedResourceDomainAiFunction, "AI_CLASSIFY"}},
		{"quoted and unquoted agent names", quotaSharedResource{sdk.QuotaSharedResourceDomainCortexAgent, `"DB"."SCH"."AGENT"`}, quotaSharedResource{sdk.QuotaSharedResourceDomainCortexAgent, "DB.SCH.AGENT"}},
		{"quoted and unquoted warehouse names", quotaSharedResource{sdk.QuotaSharedResourceDomainWarehouse, `"WH"`}, quotaSharedResource{sdk.QuotaSharedResourceDomainWarehouse, "WH"}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.a.key(), tc.b.key())
		})
	}

	assert.NotEqual(t,
		quotaSharedResource{domain: sdk.QuotaSharedResourceDomainAiFunction}.key(),
		quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexCode}.key(),
	)
}

func Test_quotaDiffByKey(t *testing.T) {
	removed, added := quotaDiffByKey([]string{"a", "b"}, []string{"b", "c"}, quotaUserKey)
	assert.Equal(t, []string{"a"}, removed)
	assert.Equal(t, []string{"c"}, added)

	assert.True(t, quotaKeysEqual([]string{`"alice"`}, []string{"alice"}, quotaUserKey))
	assert.True(t, quotaKeysEqual([]string{"Admin@Example.com"}, []string{"admin@example.com"}, quotaEmailKey))
}

func Test_quotaCustomAction_removalKey(t *testing.T) {
	procedure := sdk.NewSchemaObjectIdentifierWithArguments("DB", "SCH", "PROC", sdk.DataTypeVARCHAR)
	actual := quotaCustomAction{procedure: procedure, spendStrategy: sdk.QuotaSpendStrategyActual, threshold: 100}
	projected := quotaCustomAction{procedure: procedure, spendStrategy: sdk.QuotaSpendStrategyProjected, threshold: 100}

	assert.Equal(t, actual.removalKey(), projected.removalKey())
	assert.NotEqual(t, actual.key(), projected.key())
}
