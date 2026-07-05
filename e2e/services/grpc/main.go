// A minimal gRPC server exposing the standard health service, so any language's
// gRPC client can prove it reached us BY NAME with a Health/Check → SERVING (no
// custom .proto or per-language stubs needed).
package main

import (
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	ln, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	h := health.NewServer()
	grpc_health_v1.RegisterHealthServer(s, h)
	h.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	log.Println("grpc health server on :50051")
	if err := s.Serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
