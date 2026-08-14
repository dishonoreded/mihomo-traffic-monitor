package api

func openAPISpecification() map[string]any {
	statusReference := map[string]any{"$ref": "#/components/schemas/Status"}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "mihomo-monitor local API",
			"version": "1.0.0",
		},
		"paths": map[string]any{
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
			"/api/v1/openapi.json": map[string]any{
				"get": map[string]any{
					"operationId": "getOpenAPI",
					"responses":   map[string]any{"200": map[string]any{"description": "OpenAPI document"}},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				"Status": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "timestamp", "collector", "live", "database", "configuration"},
					"properties": map[string]any{
						"apiVersion": map[string]any{"type": "string", "const": "v1"},
						"timestamp":  map[string]any{"type": "string", "format": "date-time"},
						"collector": map[string]any{
							"type":     "object",
							"required": []string{"state", "reason", "message", "lastSample"},
							"properties": map[string]any{
								"state":      map[string]any{"type": "string", "enum": []string{"unavailable", "connecting", "connected"}},
								"reason":     map[string]any{"type": "string"},
								"message":    map[string]any{"type": "string"},
								"lastSample": map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
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
