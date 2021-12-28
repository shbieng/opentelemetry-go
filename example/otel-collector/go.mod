module go.opentelemetry.io/otel/example/otel-collector

go 1.14

replace (
	go.opentelemetry.io/otel => ../..
	go.opentelemetry.io/otel/exporters/otlp => ../../exporters/otlp
	go.opentelemetry.io/otel/sdk => ../../sdk
)

require (
	go.opentelemetry.io/otel v0.10.0
	go.opentelemetry.io/otel/exporters/otlp v0.10.0
	go.opentelemetry.io/otel/sdk v0.10.0
	golang.org/x/net v0.0.0-20200114155413-6afb5195e5aa // indirect
	google.golang.org/grpc v1.31.0
)
