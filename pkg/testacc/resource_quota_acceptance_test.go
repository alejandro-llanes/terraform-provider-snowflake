//go:build non_account_level_tests

package testacc

import (
	"regexp"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/bettertestspoc/assert"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/bettertestspoc/assert/resourceassert"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/bettertestspoc/assert/resourceshowoutputassert"
	accconfig "github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/bettertestspoc/config"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/bettertestspoc/config/model"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/helpers"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/helpers/random"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/acceptance/planchecks"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/provider/resources"
	"github.com/Snowflake-Labs/terraform-provider-snowflake/pkg/sdk"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// TODO [quota spike]: the quota acceptance tests follow the per-user quotas guide and have not been run against a live account yet.

func grantProcedureToSnowflakeApplication(t *testing.T, procedureId sdk.SchemaObjectIdentifierWithArguments) {
	t.Helper()
	t.Cleanup(testClient().Grant.GrantUsageOnDatabaseToSnowflakeApplication(t, procedureId.DatabaseId()))
	t.Cleanup(testClient().Grant.GrantUsageOnSchemaToSnowflakeApplication(t, procedureId.SchemaId()))
	t.Cleanup(testClient().Grant.GrantUsageOnProcedureToSnowflakeApplication(t, procedureId))
}

func TestAcc_Quota_BasicUseCase(t *testing.T) {
	user, userCleanup := testClient().User.CreateUser(t)
	t.Cleanup(userCleanup)

	id := testClient().Ids.RandomSchemaObjectIdentifier()

	basic := model.QuotaFromId("test", id)
	configured := model.QuotaFromId("test", id).
		WithSharedResources(
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainAiFunction},
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainCortexCode, Instance: "CORTEX_CODE_CLI"},
		).
		WithIncludedUsers(user.ID().Name()).
		WithPerUserLimitMonthly(1000).
		WithPerUserLimitDaily(50).
		WithBlockEnforcementEnabled(true).
		WithNotificationThresholds(model.QuotaNotificationThresholdModel{
			Threshold:     80,
			SpendStrategy: sdk.QuotaSpendStrategyProjected,
			NotifyUser:    true,
			Cycle:         sdk.QuotaCycleMonthly,
		})

	ref := basic.ResourceReference()

	basicAssertions := []assert.TestCheckFuncProvider{
		resourceassert.QuotaResource(t, ref).
			HasDatabaseString(id.DatabaseName()).
			HasSchemaString(id.SchemaName()).
			HasNameString(id.Name()).
			HasFullyQualifiedNameString(id.FullyQualifiedName()).
			HasCommentString("").
			HasSharedResourcesCount(0).
			HasUserTagsEmpty().
			HasIncludedUsersEmpty().
			HasExcludedUsersEmpty().
			HasPerUserLimitMonthly(-1).
			HasPerUserLimitWeekly(-1).
			HasPerUserLimitDaily(-1).
			HasBlockEnforcementEnabled(false).
			HasNotificationThresholdsCount(0).
			HasCustomActionsCount(0).
			HasCycleStartActionEmpty(),
		resourceshowoutputassert.QuotaShowOutput(t, ref).
			HasCreatedOnNotEmpty().
			HasName(id.Name()).
			HasDatabaseName(id.DatabaseName()).
			HasSchemaName(id.SchemaName()).
			HasOwnerNotEmpty(),
	}

	configuredAssertions := []assert.TestCheckFuncProvider{
		resourceassert.QuotaResource(t, ref).
			HasNameString(id.Name()).
			HasSharedResourcesCount(2).
			HasSharedResource(sdk.QuotaSharedResourceDomainAiFunction, "").
			HasSharedResource(sdk.QuotaSharedResourceDomainCortexCode, "CORTEX_CODE_CLI").
			HasIncludedUsers(user.ID().Name()).
			HasPerUserLimitMonthly(1000).
			HasPerUserLimitWeekly(-1).
			HasPerUserLimitDaily(50).
			HasBlockEnforcementEnabled(true).
			HasNotificationThresholdsCount(1).
			HasNotificationThreshold(80, sdk.QuotaSpendStrategyProjected, true, sdk.QuotaCycleMonthly),
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		CheckDestroy: CheckDestroy(t, resources.Quota),
		Steps: []resource.TestStep{
			// create with required fields only
			{
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ref, plancheck.ResourceActionCreate),
					},
				},
				Config: accconfig.FromModels(t, basic),
				Check:  assertThat(t, basicAssertions...),
			},
			// import with required fields only
			{
				Config:            accconfig.FromModels(t, basic),
				ResourceName:      ref,
				ImportState:       true,
				ImportStateVerify: true,
			},
			// configure in place
			{
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ref, plancheck.ResourceActionUpdate),
					},
				},
				Config: accconfig.FromModels(t, configured),
				Check:  assertThat(t, configuredAssertions...),
			},
			// external change is detected and reverted
			{
				PreConfig: func() {
					testClient().Quota.SetPerUserLimit(t, id, 5000, sdk.QuotaCycleMonthly)
					testClient().Quota.AddSharedResource(t, id, sdk.QuotaSharedResourceDomainSnowflakeIntelligence, "")
				},
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ref, plancheck.ResourceActionUpdate),
						planchecks.ExpectDrift(ref, "per_user_limit_monthly", sdk.String("1000"), sdk.String("5000")),
						planchecks.ExpectChange(ref, "per_user_limit_monthly", tfjson.ActionUpdate, sdk.String("5000"), sdk.String("1000")),
					},
				},
				Config: accconfig.FromModels(t, configured),
				Check:  assertThat(t, configuredAssertions...),
			},
			// unset everything
			{
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(ref, plancheck.ResourceActionUpdate),
					},
				},
				Config: accconfig.FromModels(t, basic),
				Check:  assertThat(t, basicAssertions...),
			},
		},
	})
}

func TestAcc_Quota_CompleteUseCase(t *testing.T) {
	includedUser, includedUserCleanup := testClient().User.CreateUser(t)
	t.Cleanup(includedUserCleanup)
	excludedUser, excludedUserCleanup := testClient().User.CreateUser(t)
	t.Cleanup(excludedUserCleanup)
	tag, tagCleanup := testClient().Tag.CreateTag(t)
	t.Cleanup(tagCleanup)
	agentId := testClient().Ids.RandomSchemaObjectIdentifier()
	t.Cleanup(testClient().CortexAgent.CreateWithId(t, agentId))
	integration, integrationCleanup := testClient().NotificationIntegration.Create(t)
	t.Cleanup(integrationCleanup)
	t.Cleanup(testClient().Grant.GrantUsageOnIntegrationToSnowflakeApplication(t, integration.ID()))

	id := testClient().Ids.RandomSchemaObjectIdentifier()
	comment := random.Comment()

	complete := model.QuotaFromId("test", id).
		WithComment(comment).
		WithSharedResources(
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainAiFunction, Instance: "AI_CLASSIFY"},
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainCortexAgent, Instance: agentId.FullyQualifiedName()},
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainSnowflakeIntelligence},
		).
		WithUserTags(sdk.QuotaUserTagsOperatorIntersection, model.QuotaTagValueModel{Tag: tag.ID(), Value: "finance"}).
		WithExcludedUserTags(model.QuotaTagValueModel{Tag: tag.ID(), Value: "contractor"}).
		WithIncludedUsers(includedUser.ID().Name()).
		WithExcludedUsers(excludedUser.ID().Name()).
		WithPerUserLimitMonthly(1000).
		WithPerUserLimitWeekly(300).
		WithPerUserLimitDaily(60).
		WithBlockEnforcementEnabled(true).
		WithNotifyBlockedUsers(true).
		WithNotificationThresholds(
			model.QuotaNotificationThresholdModel{Threshold: 80, SpendStrategy: sdk.QuotaSpendStrategyProjected, NotifyUser: true, Cycle: sdk.QuotaCycleMonthly},
			model.QuotaNotificationThresholdModel{Threshold: 100, SpendStrategy: sdk.QuotaSpendStrategyActual, NotifyUser: false, Cycle: sdk.QuotaCycleMonthly},
		).
		WithNotificationIntegrations(integration.ID()).
		WithAdminEmails(helpers.VerifiedEmail)

	ref := complete.ResourceReference()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		CheckDestroy: CheckDestroy(t, resources.Quota),
		Steps: []resource.TestStep{
			{
				Config: accconfig.FromModels(t, complete),
				Check: assertThat(t,
					resourceassert.QuotaResource(t, ref).
						HasNameString(id.Name()).
						HasCommentString(comment).
						HasSharedResourcesCount(3).
						HasSharedResource(sdk.QuotaSharedResourceDomainAiFunction, "AI_CLASSIFY").
						HasSharedResource(sdk.QuotaSharedResourceDomainCortexAgent, agentId.FullyQualifiedName()).
						HasSharedResource(sdk.QuotaSharedResourceDomainSnowflakeIntelligence, "").
						HasUserTags(sdk.QuotaUserTagsOperatorIntersection, 1).
						HasUserTag(tag.ID(), "finance").
						HasExcludedUserTag(tag.ID(), "contractor").
						HasIncludedUsers(includedUser.ID().Name()).
						HasExcludedUsers(excludedUser.ID().Name()).
						HasPerUserLimitMonthly(1000).
						HasPerUserLimitWeekly(300).
						HasPerUserLimitDaily(60).
						HasBlockEnforcementEnabled(true).
						HasNotifyBlockedUsers(true).
						HasNotificationThresholdsCount(2).
						HasNotificationThreshold(80, sdk.QuotaSpendStrategyProjected, true, sdk.QuotaCycleMonthly).
						HasNotificationThreshold(100, sdk.QuotaSpendStrategyActual, false, sdk.QuotaCycleMonthly).
						HasNotificationIntegrations(integration.ID().Name()).
						HasAdminEmails(helpers.VerifiedEmail),
					resourceshowoutputassert.QuotaShowOutput(t, ref).
						HasName(id.Name()).
						HasComment(comment),
					assert.Check(resource.TestCheckResourceAttr(ref, "describe_output.0.config.0.per_user_limit", "1000")),
					assert.Check(resource.TestCheckResourceAttr(ref, "describe_output.0.scope.0.shared_resources.#", "3")),
				),
			},
			// values that can't be read back are not imported
			{
				Config:                  accconfig.FromModels(t, complete),
				ResourceName:            ref,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"admin_emails", "notify_blocked_users"},
			},
		},
	})
}

func TestAcc_Quota_Domains(t *testing.T) {
	agentId := testClient().Ids.RandomSchemaObjectIdentifier()
	t.Cleanup(testClient().CortexAgent.CreateWithId(t, agentId))
	warehouse, warehouseCleanup := testClient().Warehouse.CreateWarehouse(t)
	t.Cleanup(warehouseCleanup)

	id := testClient().Ids.RandomSchemaObjectIdentifier()
	ref := model.QuotaFromId("test", id).ResourceReference()

	// Each step replaces the monitored domain in place; the switch to the WAREHOUSE domain checks that removals happen first.
	steps := make([]resource.TestStep, 0)
	for _, sharedResource := range []model.QuotaSharedResourceModel{
		{Domain: sdk.QuotaSharedResourceDomainAiFunction},
		{Domain: sdk.QuotaSharedResourceDomainAiFunction, Instance: "AI_CLASSIFY"},
		{Domain: sdk.QuotaSharedResourceDomainCortexCode},
		{Domain: sdk.QuotaSharedResourceDomainCortexCode, Instance: "CORTEX_CODE_SNOWSIGHT"},
		{Domain: sdk.QuotaSharedResourceDomainCortexAgent},
		{Domain: sdk.QuotaSharedResourceDomainCortexAgent, Instance: agentId.FullyQualifiedName()},
		{Domain: sdk.QuotaSharedResourceDomainSnowflakeIntelligence},
		{Domain: sdk.QuotaSharedResourceDomainWarehouse, Instance: warehouse.ID().Name()},
	} {
		config := model.QuotaFromId("test", id).WithSharedResources(sharedResource)
		steps = append(steps,
			resource.TestStep{
				Config: accconfig.FromModels(t, config),
				Check: assertThat(t,
					resourceassert.QuotaResource(t, ref).
						HasSharedResourcesCount(1).
						HasSharedResource(sharedResource.Domain, sharedResource.Instance),
				),
			},
			resource.TestStep{
				Config: accconfig.FromModels(t, config),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectEmptyPlan(),
					},
				},
			},
		)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		CheckDestroy: CheckDestroy(t, resources.Quota),
		Steps:        steps,
	})
}

func TestAcc_Quota_AiGateway(t *testing.T) {
	t.Skip("TODO [quota spike]: AI Gateway is a preview domain; enable once it's available in the test accounts")

	id := testClient().Ids.RandomSchemaObjectIdentifier()
	config := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainAiGateway})
	ref := config.ResourceReference()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		CheckDestroy: CheckDestroy(t, resources.Quota),
		Steps: []resource.TestStep{
			{
				Config: accconfig.FromModels(t, config),
				Check: assertThat(t,
					resourceassert.QuotaResource(t, ref).
						HasSharedResourcesCount(1).
						HasSharedResource(sdk.QuotaSharedResourceDomainAiGateway, ""),
				),
			},
		},
	})
}

func TestAcc_Quota_WarehouseWithActions(t *testing.T) {
	warehouse, warehouseCleanup := testClient().Warehouse.CreateWarehouse(t)
	t.Cleanup(warehouseCleanup)
	actionProcedure, actionProcedureCleanup := testClient().Procedure.Create(t, sdk.DataTypeVARCHAR)
	t.Cleanup(actionProcedureCleanup)
	grantProcedureToSnowflakeApplication(t, actionProcedure.ID())
	cycleStartProcedure, cycleStartProcedureCleanup := testClient().Procedure.Create(t)
	t.Cleanup(cycleStartProcedureCleanup)
	grantProcedureToSnowflakeApplication(t, cycleStartProcedure.ID())

	id := testClient().Ids.RandomSchemaObjectIdentifier()

	withActions := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainWarehouse, Instance: warehouse.ID().Name()}).
		WithPerUserLimitMonthly(100).
		WithCustomActions(
			model.QuotaCustomActionModel{Procedure: actionProcedure.ID(), SpendStrategy: sdk.QuotaSpendStrategyActual, Threshold: 100},
			model.QuotaCustomActionModel{Procedure: actionProcedure.ID(), SpendStrategy: sdk.QuotaSpendStrategyProjected, Threshold: 100},
		).
		WithCycleStartAction(cycleStartProcedure.ID())
	// changing one of the two actions at the same threshold re-adds the other, because REMOVE_CUSTOM_ACTIONS removes both
	withChangedAction := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainWarehouse, Instance: warehouse.ID().Name()}).
		WithPerUserLimitMonthly(100).
		WithCustomActions(
			model.QuotaCustomActionModel{Procedure: actionProcedure.ID(), SpendStrategy: sdk.QuotaSpendStrategyActual, Threshold: 100},
			model.QuotaCustomActionModel{Procedure: actionProcedure.ID(), SpendStrategy: sdk.QuotaSpendStrategyProjected, Threshold: 100, Arguments: []string{"arg"}},
		)

	ref := withActions.ResourceReference()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		CheckDestroy: CheckDestroy(t, resources.Quota),
		Steps: []resource.TestStep{
			{
				Config: accconfig.FromModels(t, withActions),
				Check: assertThat(t,
					resourceassert.QuotaResource(t, ref).
						HasSharedResource(sdk.QuotaSharedResourceDomainWarehouse, warehouse.ID().Name()).
						HasPerUserLimitMonthly(100).
						HasCustomActionsCount(2).
						HasCustomAction(actionProcedure.ID(), sdk.QuotaSpendStrategyActual, 100).
						HasCustomAction(actionProcedure.ID(), sdk.QuotaSpendStrategyProjected, 100).
						HasCycleStartAction(cycleStartProcedure.ID()),
				),
			},
			{
				Config: accconfig.FromModels(t, withChangedAction),
				Check: assertThat(t,
					resourceassert.QuotaResource(t, ref).
						HasCustomActionsCount(2).
						HasCustomAction(actionProcedure.ID(), sdk.QuotaSpendStrategyActual, 100).
						HasCustomAction(actionProcedure.ID(), sdk.QuotaSpendStrategyProjected, 100).
						HasCycleStartActionEmpty(),
				),
			},
			{
				Config:            accconfig.FromModels(t, withChangedAction),
				ResourceName:      ref,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAcc_Quota_Validations(t *testing.T) {
	id := testClient().Ids.RandomSchemaObjectIdentifier()

	mixedDomains := model.QuotaFromId("test", id).
		WithSharedResources(
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainWarehouse},
			model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainAiFunction},
		)
	blockEnforcementForWarehouses := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainWarehouse}).
		WithBlockEnforcementEnabled(true)
	unqualifiedAgent := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: sdk.QuotaSharedResourceDomainCortexAgent, Instance: "AGENT"})
	lowercaseDomain := model.QuotaFromId("test", id).
		WithSharedResources(model.QuotaSharedResourceModel{Domain: "ai function"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: TestAccProtoV6ProviderFactories,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.RequireAbove(tfversion.Version1_5_0),
		},
		Steps: []resource.TestStep{
			{
				Config:      accconfig.FromModels(t, mixedDomains),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("a quota can monitor either the WAREHOUSE domain or the AI domains, not both"),
			},
			{
				Config:      accconfig.FromModels(t, blockEnforcementForWarehouses),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile("block_enforcement_enabled applies only to the AI domains"),
			},
			{
				Config:      accconfig.FromModels(t, unqualifiedAgent),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`invalid instance "AGENT" of the CORTEX AGENT domain`),
			},
			{
				Config:      accconfig.FromModels(t, lowercaseDomain),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`expected .*domain to be one of`),
			},
		},
	})
}
