// Command orderd is the fixture's entry point.
package main

import (
	"log"

	"example.com/orderd/internal/handlers"
	"example.com/orderd/internal/persistence"
	"example.com/orderd/internal/platform/config"
	"example.com/orderd/internal/services/orders"
)

func main() {
	if err := Run("config.yaml"); err != nil {
		log.Fatal(err)
	}
}

// Run wires the object graph and returns the first wiring error.
func Run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	repo := persistence.NewMemoryOrderRepository()
	svc := orders.NewOrderService(repo)
	router := handlers.NewRouter(
		handlers.NewOrderHandler(svc),
		handlers.NewHealthHandler(),
		handlers.NewAdminHandler(),
	)
	return router.Handle(cfg.Addr)
}
