# Database logins for every service except the migrator, with their passwords
# and connection strings. Migration 000048 makes the permission groups; this
# file makes the logins that join them. Passwords and the owner URL are
# ephemeral or write-only, so neither is saved in Terraform state.
#
# Logins are created with SQL through the postgresql provider, not Neon's API
# or console, which would add them to neon_superuser and its read and write on
# every table.

# The owner URL, read for this run only. Also what the migrate job uses.
ephemeral "google_secret_manager_secret_version" "owner_url" {
  secret  = data.google_secret_manager_secret.database_url.secret_id
  project = var.project_id
}

locals {
  # [user, password, host, port, database]. The password must not need URL
  # escaping; Neon's generated ones do not.
  owner = regex(
    "^postgres(?:ql)?://([^:/@]+):([^@]+)@([^/:?]+)(?::([0-9]+))?/([^?]+)",
    ephemeral.google_secret_manager_secret_version.owner_url.secret_data,
  )

  db_logins = {
    api    = { role = "story_data_api", group = "story_data_app" }
    agents = { role = "agents_service", group = "story_data_agents" }
    recs   = { role = "recs_app", group = "story_data_recs" }
  }

  # Which secrets carry each login's URL. recs reads two existing secrets, both
  # owned by the recs stack; this stack only adds their versions.
  db_login_secrets = {
    api     = { login = "api", secret = google_secret_manager_secret.database_url_app.id }
    agents  = { login = "agents", secret = google_secret_manager_secret.database_url_agents.id }
    recs_rw = { login = "recs", secret = data.google_secret_manager_secret.recs_rw.id }
    recs_ro = { login = "recs", secret = data.google_secret_manager_secret.recs_ro.id }
  }
}

provider "postgresql" {
  host            = local.owner[2]
  port            = coalesce(local.owner[3], "5432")
  database        = local.owner[4]
  username        = local.owner[0]
  password        = local.owner[1]
  sslmode         = "require"
  superuser       = false
  connect_timeout = 15
}

# A fresh value each run. Only used when db_password_version changes, and then
# the role and its secrets take the same value from the same run.
ephemeral "random_password" "db_login" {
  for_each = local.db_logins
  length   = 40
  special  = false
}

resource "postgresql_role" "login" {
  for_each            = local.db_logins
  name                = each.value.role
  login               = true
  password_wo         = ephemeral.random_password.db_login[each.key].result
  password_wo_version = var.db_password_version
  # Membership belongs to postgresql_grant_role; without this the next apply revokes it.
  lifecycle {
    ignore_changes = [roles]
  }
}

# The groups exist once the migrate job has applied 000048, which the deploy
# runs before this apply.
resource "postgresql_grant_role" "login" {
  for_each   = local.db_logins
  role       = postgresql_role.login[each.key].name
  grant_role = each.value.group
}

resource "google_secret_manager_secret" "database_url_app" {
  secret_id = "story-data-database-url-app"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "database_url_agents" {
  secret_id = "story-data-database-url-agents"
  replication {
    auto {}
  }
}

data "google_secret_manager_secret" "recs_rw" {
  secret_id = "recs-postgres-dsn-rw"
  project   = var.project_id
}

data "google_secret_manager_secret" "recs_ro" {
  secret_id = "recs-postgres-dsn-ro"
  project   = var.project_id
}

# The owner URL with the login's name and password in place of the owner's,
# so host, database, sslmode and channel binding stay identical.
resource "google_secret_manager_secret_version" "login" {
  for_each = local.db_login_secrets
  secret   = each.value.secret
  secret_data_wo = replace(
    ephemeral.google_secret_manager_secret_version.owner_url.secret_data,
    "/^(postgres(?:ql)?://)[^@]+@/",
    "$${1}${local.db_logins[each.value.login].role}:${ephemeral.random_password.db_login[each.value.login].result}@",
  )
  secret_data_wo_version = var.db_password_version

  depends_on = [postgresql_role.login]
}
