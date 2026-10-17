package bootstrap

import (
	contractsfoundation "github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/foundation"

	"goravel/config"
)

func Boot() contractsfoundation.Application {
	return foundation.Setup().
		WithProviders(Providers).
		WithConfig(config.Boot).
		Create()
}
