## Minimal
resource "snowflake_quota" "basic" {
  database = "database_name"
  schema   = "schema_name"
  name     = "quota_name"
}

## AI domains: per-user Cortex Code and AI function limits for the finance team, with blocking
resource "snowflake_quota" "ai" {
  database = "database_name"
  schema   = "schema_name"
  name     = "ai_quota"
  comment  = "per-user AI quota"

  shared_resource {
    domain = "CORTEX CODE"
  }
  shared_resource {
    domain   = "AI FUNCTION"
    instance = "AI_CLASSIFY"
  }
  shared_resource {
    domain   = "CORTEX AGENT"
    instance = snowflake_cortex_agent.example.fully_qualified_name
  }

  user_tags {
    operator = "UNION"
    tag {
      tag   = snowflake_tag.cost_center.fully_qualified_name
      value = "finance"
    }
  }
  excluded_user_tag {
    tag   = snowflake_tag.usage_tier.fully_qualified_name
    value = "high"
  }
  included_users = [snowflake_user.alice.name]
  excluded_users = [snowflake_user.carol.name]

  per_user_limit_monthly = 1000
  per_user_limit_weekly  = 300
  per_user_limit_daily   = 60

  block_enforcement_enabled = true
  notify_blocked_users      = true

  notification_threshold {
    threshold      = 80
    spend_strategy = "PROJECTED"
    notify_user    = true
    cycle          = "MONTHLY"
  }
  notification_threshold {
    threshold      = 100
    spend_strategy = "ACTUAL"
    notify_user    = false
  }

  notification_integrations = [snowflake_email_notification_integration.example.name]
  admin_emails              = ["admin@example.com"]
}

## Warehouse domain: per-user warehouse credit limits with custom and cycle-start actions
resource "snowflake_quota" "warehouse" {
  database = "database_name"
  schema   = "schema_name"
  name     = "warehouse_quota"

  shared_resource {
    domain   = "WAREHOUSE"
    instance = snowflake_warehouse.example.name
  }

  per_user_limit_monthly = 100

  # The procedures must use EXECUTE AS OWNER and be granted to the SNOWFLAKE application.
  custom_action {
    procedure      = "\"database_name\".\"schema_name\".\"disable_users\"(VARCHAR)"
    spend_strategy = "ACTUAL"
    threshold      = 100
  }

  cycle_start_action {
    procedure = "\"database_name\".\"schema_name\".\"enable_users\"()"
  }
}
