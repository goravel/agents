package bootstrap

import (
	"github.com/goravel/framework/ai"
	"github.com/goravel/framework/cache"
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/database"
	"github.com/goravel/framework/http"
	"github.com/goravel/framework/log"
	"github.com/goravel/framework/schedule"
	"github.com/goravel/openai"
	"github.com/goravel/sqlite"
)

func Providers() []foundation.ServiceProvider {
	return []foundation.ServiceProvider{
		&ai.ServiceProvider{},
		&openai.ServiceProvider{},
		&http.ServiceProvider{},
		&log.ServiceProvider{},
		&database.ServiceProvider{},
		&sqlite.ServiceProvider{},
		&cache.ServiceProvider{},
		&schedule.ServiceProvider{},
	}
}
