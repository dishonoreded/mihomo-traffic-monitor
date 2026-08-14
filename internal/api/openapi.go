package api

func openAPISpecification() map[string]any {
	statusReference := map[string]any{"$ref": "#/components/schemas/Status"}
	summaryReference := map[string]any{"$ref": "#/components/schemas/Summary"}
	timeParameter := func(name string) map[string]any {
		return map[string]any{
			"name": name, "in": "query", "required": true,
			"schema": map[string]any{"type": "string", "format": "date-time"},
		}
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "mihomo-monitor local API",
			"version": "1.0.0",
		},
		"paths": map[string]any{
			"/api/v1/summary": map[string]any{
				"get": map[string]any{
					"operationId": "getTrafficSummary",
					"summary":     "Summarize permanent minute traffic in a half-open time range",
					"parameters":  []any{timeParameter("start"), timeParameter("end")},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Directional attribution totals, coverage, and current leaders",
							"content":     map[string]any{"application/json": map[string]any{"schema": summaryReference}},
						},
						"400": map[string]any{"description": "Missing, malformed, or empty time range"},
					},
				},
			},
			"/api/v1/status": map[string]any{
				"get": map[string]any{
					"operationId": "getStatus",
					"summary":     "Inspect local collector and database status",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Local observatory status",
							"content":     map[string]any{"application/json": map[string]any{"schema": statusReference}},
						},
					},
				},
			},
			"/api/v1/live/events": map[string]any{
				"get": map[string]any{
					"operationId": "streamLiveStatus",
					"summary":     "Stream current collector status and live traffic",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Server-sent status events with periodic keepalives",
							"content": map[string]any{"text/event-stream": map[string]any{
								"schema": map[string]any{
									"type":        "string",
									"description": "Status events contain JSON matching the Status schema; comment frames are keepalives.",
								},
							}},
						},
					},
				},
			},
			"/api/v1/openapi.json": map[string]any{
				"get": map[string]any{
					"operationId": "getOpenAPI",
					"responses":   map[string]any{"200": map[string]any{"description": "OpenAPI document"}},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				"AttributionTotals": map[string]any{
					"type":     "object",
					"required": []string{"observed", "residual", "gapRecovered", "total"},
					"properties": map[string]any{
						"observed":     map[string]any{"type": "integer", "minimum": 0},
						"residual":     map[string]any{"type": "integer", "minimum": 0},
						"gapRecovered": map[string]any{"type": "integer", "minimum": 0},
						"total":        map[string]any{"type": "integer", "minimum": 0},
					},
				},
				"Leader": map[string]any{
					"type":     "object",
					"required": []string{"name", "upload", "download", "total"},
					"properties": map[string]any{
						"name":     map[string]any{"type": "string"},
						"upload":   map[string]any{"type": "integer", "minimum": 0},
						"download": map[string]any{"type": "integer", "minimum": 0},
						"total":    map[string]any{"type": "integer", "minimum": 0},
					},
				},
				"Summary": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "range", "upload", "download", "total", "coverage", "leaders"},
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string", "const": "v1"},
						"range": map[string]any{
							"type":     "object",
							"required": []string{"start", "end"},
							"properties": map[string]any{
								"start": map[string]any{"type": "string", "format": "date-time"},
								"end":   map[string]any{"type": "string", "format": "date-time"},
							},
						},
						"upload":   map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
						"download": map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
						"total":    map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
						"coverage": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
						"leaders": map[string]any{
							"type":     "object",
							"required": []string{"apps", "hosts"},
							"properties": map[string]any{
								"apps":  map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Leader"}},
								"hosts": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Leader"}},
							},
						},
					},
				},
				"Status": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "timestamp", "collector", "live", "database", "configuration"},
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string", "const": "v1"},
						"timestamp":  map[string]any{"type": "string", "format": "date-time"},
						"collector": map[string]any{
							"type":     "object",
							"required": []string{"state", "reason", "message", "controllerVersion", "lastSample"},
							"properties": map[string]any{
								"state":             map[string]any{"type": "string", "enum": []string{"unavailable", "connecting", "connected"}},
								"reason":            map[string]any{"type": "string"},
								"message":           map[string]any{"type": "string"},
								"controllerVersion": map[string]any{"type": []string{"string", "null"}},
								"lastSample":        map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
							},
						},
						"live": map[string]any{
							"type":     "object",
							"required": []string{"uploadBytesPerSecond", "downloadBytesPerSecond", "activeConnections"},
							"properties": map[string]any{
								"uploadBytesPerSecond":   map[string]any{"type": "integer", "minimum": 0},
								"downloadBytesPerSecond": map[string]any{"type": "integer", "minimum": 0},
								"activeConnections":      map[string]any{"type": "integer", "minimum": 0},
							},
						},
						"database": map[string]any{
							"type":     "object",
							"required": []string{"healthy", "sizeBytes", "schemaVersion", "journalMode", "error"},
							"properties": map[string]any{
								"healthy":       map[string]any{"type": "boolean"},
								"sizeBytes":     map[string]any{"type": "integer", "minimum": 0},
								"schemaVersion": map[string]any{"type": "integer", "minimum": 1},
								"journalMode":   map[string]any{"type": "string"},
								"error":         map[string]any{"type": []string{"string", "null"}},
							},
						},
						"configuration": map[string]any{
							"type":     "object",
							"required": []string{"controllerUrl", "controllerAuthentication", "dashboardAddress", "sampleInterval", "databasePath"},
							"properties": map[string]any{
								"controllerUrl":            map[string]any{"type": "string", "format": "uri"},
								"controllerAuthentication": map[string]any{"type": "string", "enum": []string{"configured", "not_configured"}},
								"dashboardAddress":         map[string]any{"type": "string"},
								"sampleInterval":           map[string]any{"type": "string"},
								"databasePath":             map[string]any{"type": "string"},
							},
						},
					},
				},
				"Error": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{"type": "object", "required": []string{"code", "message"}},
					},
				},
			},
		},
	}
}
