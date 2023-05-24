package echojwtx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/MicahParks/keyfunc/v2"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"
)

type actorContext struct{}

const (
	// ActorKey defines the context key an actor is stored in for an echo context
	ActorKey = "actor"
)

var (
	// ActorCtxKey defines the context key an actor is stored in for a plain context
	ActorCtxKey = actorContext{}

	// ErrJWKSURIMissing is returned when the jwks_uri field is not found in the issuer's oidc well-known configuration.
	ErrJWKSURIMissing = errors.New("jwks_uri missing from oidc provider")
)

func noopMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return next
}

// Opts defines options for the Auth middleware.
type Opts func(*Auth)

// AuthConfig provides configuration for JWT validation using JWKS.
type AuthConfig struct {
	// Logger defines the auth logger to use.
	//
	// Deprecated: use WithLogger instead. This will be removed in a future release.
	Logger *zap.Logger

	// Issuer is the Auth Issuer
	Issuer string `mapstructure:"issuer"`

	// Audience is the Auth Audience
	Audience string `mapstructure:"audience"`

	// JWTConfig configuration for handling JWT validation.
	//
	// Deprecated: use WithJWTConfig instead. This will be removed in a future release.
	JWTConfig echojwt.Config

	// KeyFuncOptions configuration for fetching JWKS.
	//
	// Deprecated: use WithKeyFuncOptions instead. This will be removed in a future release.
	KeyFuncOptions keyfunc.Options
}

// Auth handles JWT Authentication as echo middleware.
type Auth struct {
	logger *zap.Logger

	middleware echo.MiddlewareFunc

	// JWTConfig configuration for handling JWT validation.
	JWTConfig *echojwt.Config

	// KeyFuncOptions configuration for fetching JWKS.
	KeyFuncOptions *keyfunc.Options

	issuer   string
	audience string
}

// WithLogger sets the logger for the auth middleware.
func WithLogger(logger *zap.Logger) Opts {
	return func(a *Auth) {
		a.logger = logger
	}
}

// WithJWTConfig sets the JWTConfig for the auth middleware.
func WithJWTConfig(jwtConfig echojwt.Config) Opts {
	return func(a *Auth) {
		a.JWTConfig = &jwtConfig
	}
}

// WithKeyFuncOptions sets the KeyFuncOptions for the auth middleware.
func WithKeyFuncOptions(keyFuncOptions keyfunc.Options) Opts {
	return func(a *Auth) {
		a.KeyFuncOptions = &keyFuncOptions
	}
}

func (a *Auth) setup(ctx context.Context, config AuthConfig, options ...Opts) error {
	// The logger in the AuthConfig object is being deprecated.
	// During this time it is used if passed, otherwise a no-op logger is used.
	// In a future release, the logger will be removed from the AuthConfig object.
	// Until then the etup function will try to use the logger from the AuthConfig object
	// then check Options for a logger, and finally use a no-op logger if it is still nil.
	// The last check is to ensure that the logger is never nil, if ta nil point is
	// passed by mistake.
	if config.Logger != nil {
		a.logger = config.Logger
	} else {
		a.logger = zap.NewNop()
	}

	for _, opt := range options {
		opt(a)
	}

	if a.logger == nil {
		a.logger = zap.NewNop()
	}

	a.issuer = config.Issuer
	a.audience = config.Audience

	// While the JWTConfig is being deprecated, it is still used
	// if passed and the with function is not used
	if a.JWTConfig == nil {
		a.JWTConfig = &config.JWTConfig
	}

	jwtConfig := a.JWTConfig

	if a.KeyFuncOptions == nil {
		a.KeyFuncOptions = &config.KeyFuncOptions
	}

	keyFuncOptions := *a.KeyFuncOptions

	if jwtConfig.KeyFunc == nil {
		jwksURI, err := jwksURI(ctx, a.issuer)
		if err != nil {
			return err
		}

		jwks, err := keyfunc.Get(jwksURI, keyFuncOptions)
		if err != nil {
			return err
		}

		jwtConfig.KeyFunc = jwks.Keyfunc
	}

	mdw, err := jwtConfig.ToMiddleware()
	if err != nil {
		return err
	}

	// intercepts the next function to run final validation.
	a.middleware = func(next echo.HandlerFunc) echo.HandlerFunc {
		skipper := jwtConfig.Skipper
		if skipper == nil {
			skipper = middleware.DefaultSkipper
		}

		postActions := func(c echo.Context) error {
			if skipper(c) {
				return next(c)
			}

			if err := a.jwtHandler(c); err != nil {
				return err
			}

			return next(c)
		}

		return mdw(postActions)
	}

	return nil
}

// Middleware returns echo middleware for validation jwt tokens.
func (a *Auth) Middleware() echo.MiddlewareFunc {
	if a == nil || a.middleware == nil {
		return noopMiddleware
	}

	return a.middleware
}

// NewJWTAuth creates a new auth middleware handler for JWTs using JWKS with a logger.
func NewJWTAuth(ctx context.Context, config AuthConfig, options ...Opts) (*Auth, error) {
	auth := new(Auth)

	if err := auth.setup(ctx, config, options...); err != nil {
		return nil, err
	}

	return auth, nil
}

// NewAuth creates a new auth middleware handler for JWTs using JWKS.
//
// Deprecated: use NewJWTAuth instead. This will be removed in a future release.
// The AuthConfig shouldn't carry a logger with it.
func NewAuth(ctx context.Context, config AuthConfig) (*Auth, error) {
	return NewJWTAuth(ctx, config)
}

func jwksURI(ctx context.Context, issuer string) (string, error) {
	uri, err := url.JoinPath(issuer, ".well-known", "openid-configuration")
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close() //nolint:errcheck // no need to check

	var m map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		return "", err
	}

	jwksURL, ok := m["jwks_uri"]
	if !ok {
		return "", ErrJWKSURIMissing
	}

	return jwksURL.(string), nil
}
