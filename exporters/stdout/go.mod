module go.opentelemetry.io/otel/exporters/stdout

go 1.14

replace (
	go.opentelemetry.io/otel => ../..
	go.opentelemetry.io/otel/sdk => ../../sdk/
)

require (
	github.com/stretchr/testify v1.6.1
	go.opentelemetry.io/otel v0.11.0
	go.opentelemetry.io/otel/sdk v0.11.0
	google.golang.org/grpc v1.32.0
)
