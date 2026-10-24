//go:build non_account_level_tests

package testint

import (
	"slices"
	"testing"

	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/helpers"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TODO [quota spike]: these tests follow the per-user quotas guide and have not been run against a live account yet.
func TestInt_Quotas(t *testing.T) {
	client := testClient(t)
	ctx := testContext(t)

	sharedResourceNamed := func(t *testing.T, scope sdk.QuotaScope, domain sdk.QuotaSharedResourceDomain) []sdk.QuotaScopeSharedResource {
		t.Helper()
		return slices.DeleteFunc(slices.Clone(scope.SharedResources), func(r sdk.QuotaScopeSharedResource) bool {
			normalized, err := r.NormalizedDomain()
			return err != nil || normalized != domain
		})
	}
	userNames := func(users []sdk.QuotaScopeUser) []string {
		names := make([]string, len(users))
		for i, user := range users {
			names[i] = user.UserName
		}
		return names
	}
	describe := func(t *testing.T, id sdk.SchemaObjectIdentifier) *sdk.QuotaDetails {
		t.Helper()
		details, err := client.Quotas.DescribeDetails(ctx, id)
		require.NoError(t, err)
		return details
	}
	// quotaScopedToNewUser creates a quota that only applies to a new user, so that enabling enforcement can't block anyone else.
	quotaScopedToNewUser := func(t *testing.T) (sdk.SchemaObjectIdentifier, *sdk.User) {
		t.Helper()
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		user, userCleanup := testClientHelper().User.CreateUser(t)
		t.Cleanup(userCleanup)
		testClientHelper().Quota.IncludeUsers(t, quotaId, user.ID().Name())
		return quotaId, user
	}
	grantProcedureToSnowflakeApplication := func(t *testing.T, procedureId sdk.SchemaObjectIdentifierWithArguments) {
		t.Helper()
		t.Cleanup(testClientHelper().Grant.GrantUsageOnDatabaseToSnowflakeApplication(t, procedureId.DatabaseId()))
		t.Cleanup(testClientHelper().Grant.GrantUsageOnSchemaToSnowflakeApplication(t, procedureId.SchemaId()))
		t.Cleanup(testClientHelper().Grant.GrantUsageOnProcedureToSnowflakeApplication(t, procedureId))
	}

	t.Run("create, show and drop", func(t *testing.T) {
		id := testClientHelper().Ids.RandomSchemaObjectIdentifier()
		err := client.Quotas.Create(ctx, sdk.NewCreateQuotaRequest(id).WithComment("quota comment"))
		require.NoError(t, err)
		t.Cleanup(testClientHelper().Quota.DropFunc(t, id))

		quota, err := client.Quotas.ShowByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, id.Name(), quota.Name)
		assert.Equal(t, id.DatabaseName(), quota.DatabaseName)
		assert.Equal(t, id.SchemaName(), quota.SchemaName)
		assert.Equal(t, "quota comment", *quota.Comment)
		assert.NotEmpty(t, quota.Owner)
		assert.NotEmpty(t, quota.CreatedOn)

		err = client.Quotas.Drop(ctx, sdk.NewDropQuotaRequest(id))
		require.NoError(t, err)

		_, err = client.Quotas.ShowByID(ctx, id)
		require.ErrorIs(t, err, sdk.ErrObjectNotFound)
	})

	t.Run("show with like and in", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		otherQuotaId, otherQuotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(otherQuotaCleanup)

		quotas, err := client.Quotas.Show(ctx, sdk.NewShowQuotaRequest().
			WithLike(sdk.Like{Pattern: new(quotaId.Name())}).
			WithIn(sdk.In{Schema: quotaId.SchemaId()}))
		require.NoError(t, err)
		require.Len(t, quotas, 1)
		assert.Equal(t, quotaId, quotas[0].ID())

		quotas, err = client.Quotas.Show(ctx, sdk.NewShowQuotaRequest().WithIn(sdk.In{Schema: quotaId.SchemaId()}))
		require.NoError(t, err)
		ids := make([]sdk.SchemaObjectIdentifier, len(quotas))
		for i, quota := range quotas {
			ids[i] = quota.ID()
		}
		assert.Contains(t, ids, quotaId)
		assert.Contains(t, ids, otherQuotaId)
	})

	t.Run("describe details of a new quota", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)

		details := describe(t, quotaId)
		assert.Equal(t, quotaId, details.Id)
		assert.Empty(t, details.Scope.SharedResources)
		assert.Empty(t, details.Scope.IncludedUsers)
		assert.Empty(t, details.Scope.ExcludedUsers)
		assert.Nil(t, details.Config.PerUserLimit)
		assert.Empty(t, details.NotificationThresholds)
		assert.Empty(t, details.NotificationIntegrations)
		assert.Empty(t, details.CustomActions)
		assert.Nil(t, details.CycleStartAction)
	})

	t.Run("AI domains as shared resources", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)

		for _, domain := range []sdk.QuotaSharedResourceDomain{
			sdk.QuotaSharedResourceDomainCortexCode,
			sdk.QuotaSharedResourceDomainSnowflakeIntelligence,
		} {
			testClientHelper().Quota.AddSharedResource(t, quotaId, domain, "")
		}
		testClientHelper().Quota.AddSharedResource(t, quotaId, sdk.QuotaSharedResourceDomainAiFunction, "AI_CLASSIFY")

		scope := describe(t, quotaId).Scope
		require.Len(t, sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainCortexCode), 1)
		assert.True(t, sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainCortexCode)[0].IsWholeDomain())
		require.Len(t, sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainSnowflakeIntelligence), 1)
		require.Len(t, sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainAiFunction), 1)
		aiFunction := sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainAiFunction)[0]
		assert.False(t, aiFunction.IsWholeDomain())
		assert.Equal(t, "AI_CLASSIFY", aiFunction.Name)

		args, err := sdk.NewQuotaRemoveSharedResourceArgsRequestFromInstance(sdk.QuotaSharedResourceDomainAiFunction, "AI_CLASSIFY")
		require.NoError(t, err)
		_, err = client.Quotas.RemoveSharedResource(ctx, sdk.NewRemoveSharedResourceQuotaRequest(quotaId, *args))
		require.NoError(t, err)

		scope = describe(t, quotaId).Scope
		assert.Empty(t, sharedResourceNamed(t, scope, sdk.QuotaSharedResourceDomainAiFunction))
		assert.Len(t, scope.SharedResources, 2)
	})

	t.Run("Cortex Agent as a shared resource", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		agentId := testClientHelper().Ids.RandomSchemaObjectIdentifier()
		t.Cleanup(testClientHelper().CortexAgent.CreateWithId(t, agentId))

		testClientHelper().Quota.AddSharedResource(t, quotaId, sdk.QuotaSharedResourceDomainCortexAgent, agentId.FullyQualifiedName())

		agents := sharedResourceNamed(t, describe(t, quotaId).Scope, sdk.QuotaSharedResourceDomainCortexAgent)
		require.Len(t, agents, 1)
		assert.False(t, agents[0].IsWholeDomain())
		assert.Contains(t, agents[0].Name, agentId.Name())
	})

	t.Run("warehouse as a shared resource", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		warehouse, warehouseCleanup := testClientHelper().Warehouse.CreateWarehouse(t)
		t.Cleanup(warehouseCleanup)

		testClientHelper().Quota.AddSharedResource(t, quotaId, sdk.QuotaSharedResourceDomainWarehouse, warehouse.ID().FullyQualifiedName())

		warehouses := sharedResourceNamed(t, describe(t, quotaId).Scope, sdk.QuotaSharedResourceDomainWarehouse)
		require.Len(t, warehouses, 1)
		assert.Equal(t, warehouse.ID().Name(), warehouses[0].Name)

		args, err := sdk.NewQuotaRemoveSharedResourceArgsRequestFromInstance(sdk.QuotaSharedResourceDomainWarehouse, warehouse.ID().FullyQualifiedName())
		require.NoError(t, err)
		_, err = client.Quotas.RemoveSharedResource(ctx, sdk.NewRemoveSharedResourceQuotaRequest(quotaId, *args))
		require.NoError(t, err)

		assert.Empty(t, describe(t, quotaId).Scope.SharedResources)
	})

	t.Run("user scope", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		includedUser, includedUserCleanup := testClientHelper().User.CreateUser(t)
		t.Cleanup(includedUserCleanup)
		excludedUser, excludedUserCleanup := testClientHelper().User.CreateUser(t)
		t.Cleanup(excludedUserCleanup)
		tag, tagCleanup := testClientHelper().Tag.CreateTag(t)
		t.Cleanup(tagCleanup)

		_, err := client.Quotas.IncludeUsers(ctx, sdk.NewIncludeUsersQuotaRequest(quotaId, *sdk.NewQuotaIncludeUsersArgsRequest([]string{includedUser.ID().Name()})))
		require.NoError(t, err)
		_, err = client.Quotas.ExcludeUsers(ctx, sdk.NewExcludeUsersQuotaRequest(quotaId, *sdk.NewQuotaExcludeUsersArgsRequest().
			WithByUser(*sdk.NewQuotaExcludeUsersByUserRequest([]string{excludedUser.ID().Name()}))))
		require.NoError(t, err)
		_, err = client.Quotas.SetUserTags(ctx, sdk.NewSetUserTagsQuotaRequest(quotaId, *sdk.NewQuotaSetUserTagsArgsRequest(
			[]sdk.QuotaUserTagRequest{*sdk.NewQuotaUserTagRequest(tag.ID(), "finance")},
		).WithOperator(sdk.QuotaUserTagsOperatorIntersection)))
		require.NoError(t, err)
		_, err = client.Quotas.ExcludeUsers(ctx, sdk.NewExcludeUsersQuotaRequest(quotaId, *sdk.NewQuotaExcludeUsersArgsRequest().
			WithByTag(*sdk.NewQuotaExcludeUsersByTagRequest([]sdk.QuotaExcludedUserTagRequest{*sdk.NewQuotaExcludedUserTagRequest(tag.ID(), "contractor")}))))
		require.NoError(t, err)

		scope := describe(t, quotaId).Scope
		assert.Equal(t, []string{includedUser.ID().Name()}, userNames(scope.IncludedUsers))
		assert.Equal(t, []string{excludedUser.ID().Name()}, userNames(scope.ExcludedUsers))
		require.NotNil(t, scope.UserTags)
		assert.Equal(t, string(sdk.QuotaUserTagsOperatorIntersection), scope.UserTags.Operator)
		require.NotEmpty(t, scope.UserTags.Tags)
		assert.Equal(t, tag.ID(), scope.UserTags.Tags[0].TagId())

		// empty arrays clear the user lists
		_, err = client.Quotas.IncludeUsers(ctx, sdk.NewIncludeUsersQuotaRequest(quotaId, *sdk.NewQuotaIncludeUsersArgsRequest(nil)))
		require.NoError(t, err)
		_, err = client.Quotas.ExcludeUsers(ctx, sdk.NewExcludeUsersQuotaRequest(quotaId, *sdk.NewQuotaExcludeUsersArgsRequest().
			WithByUser(*sdk.NewQuotaExcludeUsersByUserRequest(nil))))
		require.NoError(t, err)

		scope = describe(t, quotaId).Scope
		assert.Empty(t, scope.IncludedUsers)
		assert.Empty(t, scope.ExcludedUsers)

		// TODO [quota spike]: verify that an empty array clears the user tags
		_, err = client.Quotas.SetUserTags(ctx, sdk.NewSetUserTagsQuotaRequest(quotaId, *sdk.NewQuotaSetUserTagsArgsRequest(nil).WithOperator(sdk.QuotaUserTagsOperatorUnion)))
		require.NoError(t, err)

		scope = describe(t, quotaId).Scope
		if scope.UserTags != nil {
			assert.Empty(t, scope.UserTags.Tags)
		}
	})

	t.Run("per-user limits and block enforcement", func(t *testing.T) {
		quotaId, _ := quotaScopedToNewUser(t)
		testClientHelper().Quota.AddSharedResource(t, quotaId, sdk.QuotaSharedResourceDomainAiFunction, "")

		testClientHelper().Quota.SetPerUserLimit(t, quotaId, 1000, sdk.QuotaCycleMonthly)
		testClientHelper().Quota.SetPerUserLimit(t, quotaId, 500, sdk.QuotaCycleWeekly)
		testClientHelper().Quota.SetPerUserLimit(t, quotaId, 120, sdk.QuotaCycleDaily)
		_, err := client.Quotas.SetBlockEnforcementEnabled(ctx, sdk.NewSetBlockEnforcementEnabledQuotaRequest(quotaId, *sdk.NewQuotaSetBlockEnforcementEnabledArgsRequest(true).WithNotifyUsers(false)))
		require.NoError(t, err)

		limit := func(config sdk.QuotaConfig, cycle sdk.QuotaCycle) *float64 {
			value, err := config.PerUserLimitFor(cycle)
			require.NoError(t, err)
			return value
		}

		config := describe(t, quotaId).Config
		assert.Equal(t, new(1000.0), limit(config, sdk.QuotaCycleMonthly))
		assert.Equal(t, new(500.0), limit(config, sdk.QuotaCycleWeekly))
		assert.Equal(t, new(120.0), limit(config, sdk.QuotaCycleDaily))
		assert.Equal(t, new(true), config.BlockEnforcementEnabled)

		_, err = client.Quotas.UnsetPerUserLimit(ctx, sdk.NewUnsetPerUserLimitQuotaRequest(quotaId, *sdk.NewQuotaUnsetPerUserLimitArgsRequest(sdk.QuotaCycleWeekly)))
		require.NoError(t, err)
		_, err = client.Quotas.SetBlockEnforcementEnabled(ctx, sdk.NewSetBlockEnforcementEnabledQuotaRequest(quotaId, *sdk.NewQuotaSetBlockEnforcementEnabledArgsRequest(false)))
		require.NoError(t, err)

		config = describe(t, quotaId).Config
		assert.Equal(t, new(1000.0), limit(config, sdk.QuotaCycleMonthly))
		assert.Nil(t, limit(config, sdk.QuotaCycleWeekly))
		assert.Equal(t, new(false), config.BlockEnforcementEnabled)
	})

	t.Run("notification thresholds", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)

		_, err := client.Quotas.AddNotificationThreshold(ctx, sdk.NewAddNotificationThresholdQuotaRequest(quotaId,
			*sdk.NewQuotaAddNotificationThresholdArgsRequest(80, sdk.QuotaSpendStrategyProjected, true, sdk.QuotaCycleMonthly)))
		require.NoError(t, err)
		_, err = client.Quotas.AddNotificationThreshold(ctx, sdk.NewAddNotificationThresholdQuotaRequest(quotaId,
			*sdk.NewQuotaAddNotificationThresholdArgsRequest(100, sdk.QuotaSpendStrategyActual, false, sdk.QuotaCycleDaily)))
		require.NoError(t, err)

		thresholds, err := client.Quotas.GetNotificationThresholds(ctx, sdk.NewGetNotificationThresholdsQuotaRequest(quotaId))
		require.NoError(t, err)
		require.Len(t, thresholds, 2)
		assert.True(t, slices.ContainsFunc(thresholds, func(threshold sdk.QuotaNotificationThreshold) bool {
			return threshold.Threshold == 80 && threshold.SpendStrategy == sdk.QuotaSpendStrategyProjected && threshold.NotifyUser
		}))

		_, err = client.Quotas.RemoveNotificationThreshold(ctx, sdk.NewRemoveNotificationThresholdQuotaRequest(quotaId,
			*sdk.NewQuotaRemoveNotificationThresholdArgsRequest(80, sdk.QuotaSpendStrategyProjected)))
		require.NoError(t, err)

		thresholds, err = client.Quotas.GetNotificationThresholds(ctx, sdk.NewGetNotificationThresholdsQuotaRequest(quotaId))
		require.NoError(t, err)
		require.Len(t, thresholds, 1)
		assert.Equal(t, 100, thresholds[0].Threshold)
		assert.Equal(t, sdk.QuotaSpendStrategyActual, thresholds[0].SpendStrategy)
	})

	t.Run("notification integrations and admin emails", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		integration, integrationCleanup := testClientHelper().NotificationIntegration.Create(t)
		t.Cleanup(integrationCleanup)
		t.Cleanup(testClientHelper().Grant.GrantUsageOnIntegrationToSnowflakeApplication(t, integration.ID()))

		_, err := client.Quotas.AddNotificationIntegration(ctx, sdk.NewAddNotificationIntegrationQuotaRequest(quotaId, *sdk.NewQuotaAddNotificationIntegrationArgsRequest(integration.ID())))
		require.NoError(t, err)
		_, err = client.Quotas.SetAdminEmails(ctx, sdk.NewSetAdminEmailsQuotaRequest(quotaId, *sdk.NewQuotaSetAdminEmailsArgsRequestFromEmails(helpers.VerifiedEmail)))
		require.NoError(t, err)

		integrations, err := client.Quotas.GetNotificationIntegrations(ctx, sdk.NewGetNotificationIntegrationsQuotaRequest(quotaId))
		require.NoError(t, err)
		require.Len(t, integrations, 1)
		assert.Equal(t, integration.ID().Name(), integrations[0].IntegrationName)

		_, err = client.Quotas.RemoveNotificationIntegration(ctx, sdk.NewRemoveNotificationIntegrationQuotaRequest(quotaId, *sdk.NewQuotaRemoveNotificationIntegrationArgsRequest(integration.ID())))
		require.NoError(t, err)

		integrations, err = client.Quotas.GetNotificationIntegrations(ctx, sdk.NewGetNotificationIntegrationsQuotaRequest(quotaId))
		require.NoError(t, err)
		assert.Empty(t, integrations)
	})

	t.Run("custom actions", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		procedure, procedureCleanup := testClientHelper().Procedure.Create(t, sdk.DataTypeVARCHAR)
		t.Cleanup(procedureCleanup)
		grantProcedureToSnowflakeApplication(t, procedure.ID())

		_, err := client.Quotas.AddCustomAction(ctx, sdk.NewAddCustomActionQuotaRequest(quotaId,
			*sdk.NewQuotaAddCustomActionArgsRequest(procedure.ID(), nil, sdk.QuotaSpendStrategyActual, 100)))
		require.NoError(t, err)
		_, err = client.Quotas.AddCustomAction(ctx, sdk.NewAddCustomActionQuotaRequest(quotaId,
			*sdk.NewQuotaAddCustomActionArgsRequest(procedure.ID(), []string{"arg"}, sdk.QuotaSpendStrategyProjected, 90)))
		require.NoError(t, err)

		actions, err := client.Quotas.GetCustomActions(ctx, sdk.NewGetCustomActionsQuotaRequest(quotaId))
		require.NoError(t, err)
		require.Len(t, actions, 2)
		for _, action := range actions {
			procedureId, err := action.ProcedureId()
			require.NoError(t, err)
			assert.Equal(t, procedure.ID().FullyQualifiedName(), procedureId.FullyQualifiedName())
			assert.NotEmpty(t, action.ActionId)
		}

		// the procedure is removed by the PROCEDURE_FQN returned by GET_CUSTOM_ACTIONS
		_, err = client.Quotas.RemoveCustomActions(ctx, sdk.NewRemoveCustomActionsQuotaRequest(quotaId,
			*sdk.NewQuotaRemoveCustomActionsArgsRequest().WithThreshold(100).WithProcedureFqn(actions[0].ProcedureFqn)))
		require.NoError(t, err)

		actions, err = client.Quotas.GetCustomActions(ctx, sdk.NewGetCustomActionsQuotaRequest(quotaId))
		require.NoError(t, err)
		require.Len(t, actions, 1)
		assert.Equal(t, 90, actions[0].Threshold)
		assert.Equal(t, []string{"arg"}, actions[0].ProcedureArgs)

		_, err = client.Quotas.RemoveCustomActions(ctx, sdk.NewRemoveCustomActionsQuotaRequest(quotaId, *sdk.NewQuotaRemoveCustomActionsArgsRequest()))
		require.NoError(t, err)

		actions, err = client.Quotas.GetCustomActions(ctx, sdk.NewGetCustomActionsQuotaRequest(quotaId))
		require.NoError(t, err)
		assert.Empty(t, actions)
	})

	t.Run("cycle-start action", func(t *testing.T) {
		quotaId, quotaCleanup := testClientHelper().Quota.Create(t)
		t.Cleanup(quotaCleanup)
		procedure, procedureCleanup := testClientHelper().Procedure.Create(t)
		t.Cleanup(procedureCleanup)
		grantProcedureToSnowflakeApplication(t, procedure.ID())

		_, err := client.Quotas.SetCycleStartAction(ctx, sdk.NewSetCycleStartActionQuotaRequest(quotaId, *sdk.NewQuotaSetCycleStartActionArgsRequest(procedure.ID(), nil)))
		require.NoError(t, err)

		action := describe(t, quotaId).CycleStartAction
		require.NotNil(t, action)
		procedureId, err := action.ProcedureId()
		require.NoError(t, err)
		assert.Equal(t, procedure.ID().FullyQualifiedName(), procedureId.FullyQualifiedName())
		assert.Empty(t, action.ProcedureArgs)

		_, err = client.Quotas.RemoveCycleStartAction(ctx, sdk.NewRemoveCycleStartActionQuotaRequest(quotaId))
		require.NoError(t, err)

		assert.Nil(t, describe(t, quotaId).CycleStartAction)
	})
}
