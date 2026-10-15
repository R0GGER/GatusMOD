package probe

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mongooptions "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// probeDatabase connects, pings and optionally runs ?query=...: SQL for PostgreSQL and MySQL, a command
// such as "GET key" for Redis, and a JSON command such as {"ping": 1} for MongoDB. RESULT is the first value.
// CONNECTED is only true when the server accepted the login.
func probeDatabase(ctx context.Context, u *url.URL, _ Options) (Values, error) {
	query := u.Query().Get("query")
	dsn := driverURL(u)
	start := time.Now()
	var result string
	var connected bool
	var err error
	switch u.Scheme {
	case "postgres", "postgresql":
		connected, result, err = postgresCheck(ctx, dsn.String(), query)
	case "mysql":
		connected, result, err = mysqlCheck(ctx, dsn, query)
	case "redis", "rediss":
		connected, result, err = redisCheck(ctx, dsn.String(), query)
	case "mongodb", "mongodb+srv":
		connected, result, err = mongoCheck(ctx, dsn, query)
	default:
		return nil, fmt.Errorf("unsupported database %q", u.Scheme)
	}
	values := Values{"CONNECTED": connected, "RESPONSE_TIME": ms(time.Since(start))}
	if query != "" && err == nil {
		values["RESULT"] = result
	}
	return values, err
}

// driverURL is the URL without the GatusMOD parameters, which the drivers would reject.
func driverURL(u *url.URL) *url.URL {
	c := *u
	q := c.Query()
	for _, k := range ownParams {
		q.Del(k)
	}
	c.RawQuery = q.Encode()
	return &c
}

func postgresCheck(ctx context.Context, dsn, query string) (bool, string, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false, "", err
	}
	defer conn.Close(context.Background())
	if err := conn.Ping(ctx); err != nil {
		return false, "", err
	}
	if query == "" {
		return true, "", nil
	}
	var v any
	if err := conn.QueryRow(ctx, query).Scan(&v); err != nil {
		return true, "", fmt.Errorf("query: %w", err)
	}
	return true, text(v), nil
}

// mysqlConfig turns mysql://user:pass@host:3306/db?tls=true into the driver configuration.
func mysqlConfig(u *url.URL) (*mysql.Config, error) {
	cfg := mysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	cfg.Addr = net.JoinHostPort(u.Hostname(), port)
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	dsn := cfg.FormatDSN()
	if u.RawQuery != "" {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + u.RawQuery
	}
	return mysql.ParseDSN(dsn)
}

func mysqlCheck(ctx context.Context, u *url.URL, query string) (bool, string, error) {
	cfg, err := mysqlConfig(u)
	if err != nil {
		return false, "", err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return false, "", err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return false, "", err
	}
	if query == "" {
		return true, "", nil
	}
	var v any
	if err := db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		return true, "", fmt.Errorf("query: %w", err)
	}
	return true, text(v), nil
}

func redisCheck(ctx context.Context, dsn, query string) (bool, string, error) {
	opt, err := redis.ParseURL(dsn)
	if err != nil {
		return false, "", err
	}
	opt.MaxRetries = -1
	client := redis.NewClient(opt)
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		return false, "", err
	}
	if query == "" {
		return true, "", nil
	}
	fields := strings.Fields(query)
	args := make([]any, len(fields))
	for i, f := range fields {
		args[i] = f
	}
	v, err := client.Do(ctx, args...).Result()
	if err != nil && err != redis.Nil {
		return true, "", fmt.Errorf("query: %w", err)
	}
	return true, text(v), nil
}

func mongoCheck(ctx context.Context, u *url.URL, query string) (bool, string, error) {
	opts := mongooptions.Client().ApplyURI(u.String()).SetServerSelectionTimeout(deadline(ctx))
	client, err := mongo.Connect(opts)
	if err != nil {
		return false, "", err
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, nil); err != nil {
		return false, "", err
	}
	if query == "" {
		return true, "", nil
	}
	var cmd bson.D
	if err := bson.UnmarshalExtJSON([]byte(query), false, &cmd); err != nil {
		return true, "", fmt.Errorf("query must be a JSON command such as {\"ping\": 1}: %w", err)
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		db = "admin"
	}
	raw, err := client.Database(db).RunCommand(ctx, cmd).Raw()
	if err != nil {
		return true, "", fmt.Errorf("query: %w", err)
	}
	return true, raw.String(), nil
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	}
	return fmt.Sprint(v)
}
