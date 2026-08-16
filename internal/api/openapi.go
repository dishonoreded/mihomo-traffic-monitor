package api

func openAPISpecification() map[string]any {
	statusReference := map[string]any{"$ref": "#/components/schemas/Status"}
	summaryReference := map[string]any{"$ref": "#/components/schemas/Summary"}
	seriesReference := map[string]any{"$ref": "#/components/schemas/Series"}
	gapsReference := map[string]any{"$ref": "#/components/schemas/Gaps"}
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
			"/api/v1/gaps": map[string]any{
				"get": map[string]any{
					"operationId": "getCollectionGaps",
					"summary":     "List open and closed Collection gaps overlapping a half-open time range",
					"parameters":  []any{timeParameter("start"), timeParameter("end")},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Collection gap diagnostics and directional recovered totals",
							"content":     map[string]any{"application/json": map[string]any{"schema": gapsReference}},
						},
						"400": map[string]any{"description": "Missing, malformed, or empty time range"},
					},
				},
			},
			"/api/v1/summary": map[string]any{
				"get": map[string]any{
					"operationId": "getTrafficSummary",
					"summary":     "Summarize permanent minute traffic in a half-open time range",
					"parameters":  []any{timeParameter("from"), timeParameter("to")},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Directional attribution totals, coverage, and current leaders",
							"content":     map[string]any{"application/json": map[string]any{"schema": summaryReference}},
						},
						"400": map[string]any{"description": "Missing, malformed, or empty time range"},
					},
				},
			},
			"/api/v1/series": map[string]any{
				"get": map[string]any{
					"operationId": "getTrafficSeries",
					"summary":     "Read calendar-aligned traffic points from permanent minute history",
					"description": "The range is [from, to). Auto selects the finest minute, hour, or day granularity that returns at most 400 non-empty points without truncating traffic.",
					"parameters": []any{
						timeParameter("from"),
						timeParameter("to"),
						map[string]any{
							"name": "timeZone", "in": "query", "required": true,
							"description": "IANA time zone used to align hour and day buckets across daylight-saving transitions.",
							"schema":      map[string]any{"type": "string", "example": "America/New_York"},
						},
						map[string]any{
							"name": "granularity", "in": "query", "required": true,
							"schema": map[string]any{"type": "string", "enum": []string{"minute", "hour", "day", "auto"}},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Directional attribution totals at the selected granularity",
							"content":     map[string]any{"application/json": map[string]any{"schema": seriesReference}},
						},
						"400": map[string]any{"description": "Invalid range, IANA time zone, or granularity"},
						"422": map[string]any{"description": "Auto cannot represent the range within 400 daily points"},
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
				"Gap": map[string]any{
					"type":     "object",
					"required": []string{"id", "startedAt", "endedAt", "open", "reason", "disposition", "recoveredUpload", "recoveredDownload"},
					"properties": map[string]any{
						"id":                map[string]any{"type": "integer", "minimum": 1},
						"startedAt":         map[string]any{"type": "string", "format": "date-time"},
						"endedAt":           map[string]any{"type": []string{"string", "null"}, "format": "date-time"},
						"open":              map[string]any{"type": "boolean"},
						"reason":            map[string]any{"type": "string"},
						"disposition":       map[string]any{"type": "string", "enum": []string{"open", "recovered", "no_growth", "reset"}},
						"recoveredUpload":   map[string]any{"type": "integer", "minimum": 0},
						"recoveredDownload": map[string]any{"type": "integer", "minimum": 0},
					},
				},
				"Gaps": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "range", "gaps"},
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
						"gaps": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Gap"}},
					},
				},
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
				"SeriesPoint": map[string]any{
					"type":     "object",
					"required": []string{"start", "upload", "download", "total"},
					"properties": map[string]any{
						"start":    map[string]any{"type": "string", "format": "date-time"},
						"upload":   map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
						"download": map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
						"total":    map[string]any{"$ref": "#/components/schemas/AttributionTotals"},
					},
				},
				"Series": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "granularity", "pointLimit", "timeZone", "range", "points"},
					"properties": map[string]any{
						"apiVersion":  map[string]any{"type": "string", "const": "v1"},
						"granularity": map[string]any{"type": "string", "enum": []string{"minute", "hour", "day"}},
						"pointLimit":  map[string]any{"type": "integer", "const": 400},
						"timeZone":    map[string]any{"type": "string"},
						"range": map[string]any{
							"type":     "object",
							"required": []string{"from", "to"},
							"properties": map[string]any{
								"from": map[string]any{"type": "string", "format": "date-time"},
								"to":   map[string]any{"type": "string", "format": "date-time"},
							},
						},
						"points": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/SeriesPoint"}},
					},
				},
				"Status": map[string]any{
					"type":     "object",
					"required": []string{"apiVersion", "timestamp", "collector", "live", "database", "collection", "configuration"},
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
						"collection": map[string]any{
							"type":     "object",
							"required": []string{"currentGap", "recentGaps", "error"},
							"properties": map[string]any{
								"currentGap": map[string]any{"anyOf": []any{map[string]any{"$ref": "#/components/schemas/Gap"}, map[string]any{"type": "null"}}},
								"recentGaps": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Gap"}},
								"error":      map[string]any{"type": []string{"string", "null"}},
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
