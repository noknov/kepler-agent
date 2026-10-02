package observabilitysvc

import _ "embed"

//go:embed static/dashboard.html
var healthDashboardHTML string

//go:embed static/dashboard.mjs
var dashboardJS string
