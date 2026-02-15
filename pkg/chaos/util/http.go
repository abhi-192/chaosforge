package util

import (
	"encoding/json"

	"github.com/aws/aws-lambda-go/events"
)

func JSON(status int, v any) events.APIGatewayV2HTTPResponse {
	b, _ := json.Marshal(v)
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: string(b),
	}
}

func Err(status int, msg string) events.APIGatewayV2HTTPResponse {
	return JSON(status, map[string]any{"error": msg})
}
