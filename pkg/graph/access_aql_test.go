package graph

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	driver "github.com/arangodb/go-driver"
	arangohttp "github.com/arangodb/go-driver/http"
	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/proto/node/access"
	"go.uber.org/zap"
)

func TestAccessQueriesOnArango(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || !dockerReady() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("docker is required to run the permission queries")
		}
		t.Skip("docker daemon is not available")
	}
	host, stop := startArango(t)
	defer stop()

	db := openArango(t, host)
	log := zap.NewNop()
	schema.CheckAndRegisterCollections(log, db, schema.COLLECTIONS)
	for _, g := range schema.GRAPHS_SCHEMAS {
		schema.CheckAndRegisterGraph(log, db, g)
	}

	ctx := context.Background()
	accCol, err := db.Collection(ctx, schema.ACCOUNTS_COL)
	if err != nil {
		t.Fatal(err)
	}
	nsCol, err := db.Collection(ctx, schema.NAMESPACES_COL)
	if err != nil {
		t.Fatal(err)
	}
	const accKey, nsKey = "acc-live", "ns-live"
	acc := NewBlankAccountDocument(accKey)
	ns := NewBlankNamespaceDocument(nsKey)
	if _, err := accCol.CreateDocument(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if _, err := nsCol.CreateDocument(ctx, ns); err != nil {
		t.Fatal(err)
	}

	repo := NewInfinimeshCommonActionsRepo(db)
	edge := repo.GetEdgeCol(ctx, schema.ACC2NS)
	if err := repo.Link(ctx, log, edge, acc, ns, access.Level_ADMIN, access.Role_UNSET); err != nil {
		t.Fatal(err)
	}

	ok, level := repo.AccessLevel(ctx, acc, ns)
	if !ok || level != access.Level_ADMIN {
		t.Fatalf("live grant: ok=%v level=%v", ok, level)
	}
	loaded := NewBlankNamespaceDocument(nsKey)
	if err := repo.AccessLevelAndGet(ctx, log, acc, loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.GetAccess().GetLevel() != access.Level_ADMIN {
		t.Fatalf("loaded level %v", loaded.GetAccess().GetLevel())
	}
	cur, err := repo.ListQuery(ctx, log, acc, schema.NAMESPACES_COL)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close()
	var row map[string]any
	if _, err := cur.ReadDocument(ctx, &row); err != nil {
		t.Fatal(err)
	}
	if row["_key"] != nsKey {
		t.Fatalf("list row %#v", row)
	}
	if _, err := db.Query(ctx, countDevicesQuery, map[string]any{
		"ns":          schema.NAMESPACES_COL + "/" + nsKey,
		"permissions": schema.PERMISSIONS_GRAPH.Name,
		"devices":     schema.DEVICES_COL,
	}); err != nil {
		t.Fatal(err)
	}
	devCol, err := db.Collection(ctx, schema.DEVICES_COL)
	if err != nil {
		t.Fatal(err)
	}
	pump := NewBlankDeviceDocument("pump")
	pump.Title = "Pump"
	pump.Tags = []string{"water"}
	if _, err := devCol.CreateDocument(ctx, pump); err != nil {
		t.Fatal(err)
	}
	ns2dev := repo.GetEdgeCol(ctx, schema.NS2DEV)
	if err := repo.Link(ctx, log, ns2dev, ns, pump, access.Level_ADMIN, access.Role_OWNER); err != nil {
		t.Fatal(err)
	}
	listed, err := ListNamespaceDevices(ctx, db, nsKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Device != "pump" || listed[0].Title != "Pump" || len(listed[0].Tags) != 1 {
		t.Fatalf("devices %#v", listed)
	}
	if _, err := ns2dev.UpdateDocument(ctx, nsKey+"-pump", map[string]any{"expires_at": int64(1)}); err != nil {
		t.Fatal(err)
	}
	listed, err = ListNamespaceDevices(ctx, db, nsKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("expired device edge still listed %#v", listed)
	}

	if _, err := edge.UpdateDocument(ctx, accKey+"-"+nsKey, map[string]any{"expires_at": int64(1)}); err != nil {
		t.Fatal(err)
	}
	ok, level = repo.AccessLevel(ctx, acc, ns)
	if ok || level != access.Level_NONE {
		t.Fatalf("expired grant: ok=%v level=%v", ok, level)
	}
}

func dockerReady() bool {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func startArango(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	name := fmt.Sprintf("infinimesh-aql-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		"-e", "ARANGO_ROOT_PASSWORD=openSesame",
		"-p", fmt.Sprintf("127.0.0.1:%d:8529", port),
		"arangodb:3.12.12").CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	stop := func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	}
	host := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, "http://"+host+"/_api/version", nil)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		req.SetBasicAuth("root", "openSesame")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return host, stop
			}
		}
		time.Sleep(time.Second)
	}
	stop()
	t.Fatal("arangodb did not become ready")
	return "", nil
}

func openArango(t *testing.T, host string) driver.Database {
	t.Helper()
	conn, err := arangohttp.NewConnection(arangohttp.ConnectionConfig{
		Endpoints: []string{"http://root:openSesame@" + host},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := driver.NewClient(driver.ClientConfig{Connection: conn})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := c.DatabaseExists(ctx, schema.DB_NAME); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := c.CreateDatabase(ctx, schema.DB_NAME, nil); err != nil {
			t.Fatal(err)
		}
	}
	db, err := c.Database(ctx, schema.DB_NAME)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
