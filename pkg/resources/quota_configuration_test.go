package resources

import (
	"encoding/json"
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
	tagId := sdk.NewSchemaObjectIdentifier("DB", "SCH", "TAG")
	current := &quotaConfiguration{
		sharedResources: []quotaSharedResource{
			{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH"."AGENT"`},
			{domain: sdk.QuotaSharedResourceDomainWarehouse, instance: `"wh"`},
		},
		excludedUserTags:   []quotaTagValue{{tag: tagId, raw: "DB.SCH.TAG", value: "b"}},
		includedUsers:      []string{"alice"},
		notifyBlockedUsers: true,
		adminEmails:        []string{"admin@example.com"},
		notificationThresholds: []quotaNotificationThreshold{
			{threshold: 80, spendStrategy: sdk.QuotaSpendStrategyProjected, notifyUser: true, cycle: sdk.QuotaCycleWeekly},
		},
	}
	details := &sdk.QuotaDetails{
		Scope: sdk.QuotaScope{
			SharedResources: []sdk.QuotaScopeSharedResource{
				// returned by its bare name, matched to the configured fully qualified one
				{Domain: "CORTEX_AGENT", Id: "42", Name: "AGENT"},
				{Domain: "AI_FUNCTION", Id: "-1", Name: "AI FUNCTION"},
				{Domain: "WAREHOUSE", Id: "7", Name: "wh"},
				// not configured (e.g. on import): spelled as the configuration would
				{Domain: "WAREHOUSE", Id: "8", Name: "OTHER"},
			},
			IncludedUsers: []sdk.QuotaScopeUser{{UserId: "1", UserName: "ALICE"}, {UserId: "2", UserName: "BOB"}},
			UserTags: &sdk.QuotaScopeUserTags{
				Operator: "UNION",
				Tags: []sdk.QuotaScopeTag{
					{Type: "tag", TagDatabase: "DB", TagSchema: "SCH", TagKey: "TAG", TagValues: []string{"a", "b"}},
					{Type: "EXCLUDE", TagDatabase: "DB", TagSchema: "SCH", TagKey: "TAG", TagValues: []string{"c"}},
				},
			},
		},
		Config: sdk.QuotaConfig{PerUserLimit: new(json.Number("1000")), BlockEnforcementEnabled: new(true)},
		NotificationThresholds: []sdk.QuotaNotificationThreshold{
			{Threshold: 80, SpendStrategy: sdk.QuotaSpendStrategyProjected, NotifyUser: true},
			{Threshold: 100, SpendStrategy: sdk.QuotaSpendStrategyActual},
		},
	}

	remote, err := quotaConfigurationFromDetails(details, current)
	require.NoError(t, err)

	// equivalent values keep the configured spelling
	assert.Equal(t, []quotaSharedResource{
		{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH"."AGENT"`},
		{domain: sdk.QuotaSharedResourceDomainAiFunction},
		{domain: sdk.QuotaSharedResourceDomainWarehouse, instance: `"wh"`},
		{domain: sdk.QuotaSharedResourceDomainWarehouse, instance: `"OTHER"`},
	}, remote.sharedResources)
	assert.Equal(t, []string{"alice", "BOB"}, remote.includedUsers)

	// one included tag per value; tags matching a configured exclusion or marked as excluded are skipped,
	// and the exclusions themselves are kept from the configuration
	require.NotNil(t, remote.userTagsOperator)
	assert.Equal(t, sdk.QuotaUserTagsOperatorUnion, *remote.userTagsOperator)
	assert.Equal(t, []quotaTagValue{{tag: tagId, raw: `"DB"."SCH"."TAG"`, value: "a"}}, remote.userTags)
	assert.Equal(t, current.excludedUserTags, remote.excludedUserTags)

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

	// unquoted user names are case-insensitive identifiers, quoted ones are exact
	assert.True(t, quotaKeysEqual([]string{"alice"}, []string{"ALICE"}, quotaUserKey))
	assert.True(t, quotaKeysEqual([]string{`"ALICE"`}, []string{"alice"}, quotaUserKey))
	assert.False(t, quotaKeysEqual([]string{`"alice"`}, []string{"ALICE"}, quotaUserKey))
	assert.True(t, quotaKeysEqual([]string{"Admin@Example.com"}, []string{"admin@example.com"}, quotaEmailKey))
}

func Test_matchCurrentQuotaSharedResource(t *testing.T) {
	agent1 := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH1"."AGENT"`}
	agent2 := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: `"DB"."SCH2"."AGENT"`}
	whole := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent}

	t.Run("exact key match wins", func(t *testing.T) {
		remote := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: "DB.SCH2.AGENT"}
		assert.Equal(t, agent2, matchCurrentQuotaSharedResource([]quotaSharedResource{agent1, agent2}, remote))
	})

	t.Run("unique bare name match", func(t *testing.T) {
		remote := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: "agent"}
		assert.Equal(t, agent1, matchCurrentQuotaSharedResource([]quotaSharedResource{agent1, whole}, remote))
	})

	t.Run("ambiguous bare name is kept as returned", func(t *testing.T) {
		remote := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent, instance: "AGENT"}
		assert.Equal(t, remote, matchCurrentQuotaSharedResource([]quotaSharedResource{agent1, agent2}, remote))
	})

	t.Run("whole domain never matches an instance", func(t *testing.T) {
		remote := quotaSharedResource{domain: sdk.QuotaSharedResourceDomainCortexAgent}
		assert.Equal(t, remote, matchCurrentQuotaSharedResource([]quotaSharedResource{agent1}, remote))
	})
}

func Test_quotaReconcileAccumulatingList(t *testing.T) {
	record := func(calls *[][]string) func([]string) error {
		return func(values []string) error {
			*calls = append(*calls, values)
			return nil
		}
	}

	t.Run("no change makes no call", func(t *testing.T) {
		var calls [][]string
		require.NoError(t, quotaReconcileAccumulatingList([]string{"A"}, []string{`"A"`}, quotaUserKey, record(&calls)))
		assert.Empty(t, calls)
	})

	t.Run("additions only send the added elements", func(t *testing.T) {
		var calls [][]string
		require.NoError(t, quotaReconcileAccumulatingList([]string{"A"}, []string{"A", "B"}, quotaUserKey, record(&calls)))
		assert.Equal(t, [][]string{{"B"}}, calls)
	})

	t.Run("a removal clears the list and sets the remaining elements", func(t *testing.T) {
		var calls [][]string
		require.NoError(t, quotaReconcileAccumulatingList([]string{"A", "B"}, []string{"B", "C"}, quotaUserKey, record(&calls)))
		assert.Equal(t, [][]string{{}, {"B", "C"}}, calls)
	})

	t.Run("removing everything only clears the list", func(t *testing.T) {
		var calls [][]string
		require.NoError(t, quotaReconcileAccumulatingList([]string{"A"}, nil, quotaUserKey, record(&calls)))
		assert.Equal(t, [][]string{{}}, calls)
	})
}

func Test_quotaCustomAction_removalKey(t *testing.T) {
	procedure := sdk.NewSchemaObjectIdentifierWithArguments("DB", "SCH", "PROC", sdk.DataTypeVARCHAR)
	actual := quotaCustomAction{procedure: procedure, spendStrategy: sdk.QuotaSpendStrategyActual, threshold: 100}
	projected := quotaCustomAction{procedure: procedure, spendStrategy: sdk.QuotaSpendStrategyProjected, threshold: 100}

	assert.Equal(t, actual.removalKey(), projected.removalKey())
	assert.NotEqual(t, actual.key(), projected.key())
}
