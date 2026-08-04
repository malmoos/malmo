package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// rankPtr is the *int helper the featured fixtures need (Rank is a pointer so an
// unranked app omits the field).
func rankPtr(i int) *int { return &i }

// segApps is the segmentation fixture: two featured apps at different ranks across
// different surfaces, plus one unfeatured app, so featured order, env filtering,
// and the category/search projections all have something to bite on.
//
//	alpha — appliance+hosted, categories {tools, media}, featured rank 2
//	beta  — hosted only,      categories {media},        featured rank 1
//	gamma — appliance+hosted, categories {tools},        not featured
func segApps() []wireApp {
	return []wireApp{
		{
			ID: "alpha", Name: "Alpha", Version: "1.0",
			ShortDescription: "the first app",
			Categories:       []string{"tools", "media"},
			IconFile:         "icon.png",
			Environments:     []string{"appliance", "hosted"},
			Featured:         true, Rank: rankPtr(2),
			Manifest: validManifest("alpha", "Alpha"),
			Compose:  "services:\n  web:\n    image: alpha:1\n",
		},
		{
			ID: "beta", Name: "Beta", Version: "2.0",
			ShortDescription: "streams media",
			Categories:       []string{"media"},
			Environments:     []string{"hosted"},
			Featured:         true, Rank: rankPtr(1),
			Manifest: validManifest("beta", "Beta"),
			Compose:  "services:\n  web:\n    image: beta:2\n",
		},
		{
			ID: "gamma", Name: "Gamma", Version: "3.0",
			ShortDescription: "a utility",
			Categories:       []string{"tools"},
			Environments:     []string{"appliance", "hosted"},
			Manifest:         validManifest("gamma", "Gamma"),
			Compose:          "services:\n  web:\n    image: gamma:3\n",
		},
	}
}

// syncedCatalog builds the Catalog facade over a freshly synced remote source for
// env, so the segmented methods (which live on the facade) run against a real
// snapshot without a background loop racing them.
func syncedCatalog(t *testing.T, apps []wireApp, env string) *Catalog {
	t.Helper()
	cp := newFakeCP(t, apps)
	srv := cp.server()
	t.Cleanup(srv.Close)
	rs := newRemote(srv.URL, env, t.TempDir())
	if err := rs.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	return &Catalog{src: rs}
}

func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

// homeFixture is the authored landing-page fixture for the home-projection
// tests: a hosted-only spotlight (dropped on appliance) and two groups, one of
// which (media) empties out entirely on appliance because its only app is
// hosted-only.
func homeFixture() wireHomePage {
	return wireHomePage{
		Spotlight: "beta",
		Groups: []wireHomeGroup{
			{Category: "tools", Apps: []string{"alpha", "gamma"}},
			{Category: "media", Apps: []string{"beta"}},
		},
	}
}

// makeSnapshotHome is makeSnapshot plus a home block, for the tests that
// exercise the landing-page projection.
func makeSnapshotHome(t *testing.T, apps []wireApp, home wireHomePage) (body []byte, etag string) {
	t.Helper()
	digest, err := indexDigest(apps)
	if err != nil {
		t.Fatal(err)
	}
	f := catalogFile{SchemaVersion: wireSchemaVersion, IndexSHA256: digest, Apps: apps, Home: home}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b, `"` + digest + `"`
}

// newFakeCPHome is newFakeCP plus a home block on the served snapshot.
func newFakeCPHome(t *testing.T, apps []wireApp, home wireHomePage) *fakeCP {
	body, etag := makeSnapshotHome(t, apps, home)
	return &fakeCP{body: body, etag: etag, asset: []byte("\x89PNG-fake-bytes")}
}

// syncedCatalogWithHome is syncedCatalog plus a home block on the served
// snapshot.
func syncedCatalogWithHome(t *testing.T, apps []wireApp, home wireHomePage, env string) *Catalog {
	t.Helper()
	cp := newFakeCPHome(t, apps, home)
	srv := cp.server()
	t.Cleanup(srv.Close)
	rs := newRemote(srv.URL, env, t.TempDir())
	if err := rs.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	return &Catalog{src: rs}
}

func TestHomeSegmentsByEnv(t *testing.T) {
	// Appliance: categories are the union over appliance-visible apps (alpha,
	// gamma); featured is only the appliance-visible featured app (alpha) — beta is
	// hosted-only and must not leak into the appliance landing.
	appliance, err := syncedCatalog(t, segApps(), "appliance").Home()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"media", "tools"}; !reflect.DeepEqual(appliance.Categories, want) {
		t.Fatalf("appliance categories = %v, want %v (sorted union)", appliance.Categories, want)
	}
	if got := ids(appliance.Featured); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("appliance featured = %v, want [alpha] (beta is hosted-only)", got)
	}

	// Hosted: both featured apps show, ascending by rank (beta rank 1 before alpha
	// rank 2), regardless of name order.
	hosted, err := syncedCatalog(t, segApps(), "hosted").Home()
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(hosted.Featured); !reflect.DeepEqual(got, []string{"beta", "alpha"}) {
		t.Fatalf("hosted featured = %v, want [beta alpha] (by rank)", got)
	}
}

// TestHomeProjectsSpotlightAndGroups covers the authored landing page (store's
// home.yml, carried on the synced snapshot): the spotlight and each group's
// apps are filtered to this box's environment exactly like every other
// projection — an app not advertised here drops out of its slot (the
// spotlight goes nil, or the app is skipped within its group), and a group
// left with no advertised apps is dropped entirely rather than rendered empty.
func TestHomeProjectsSpotlightAndGroups(t *testing.T) {
	home := homeFixture()

	appliance, err := syncedCatalogWithHome(t, segApps(), home, "appliance").Home()
	if err != nil {
		t.Fatal(err)
	}
	if appliance.Spotlight != nil {
		t.Fatalf("appliance spotlight = %+v, want nil (beta is hosted-only)", appliance.Spotlight)
	}
	if len(appliance.Groups) != 1 || appliance.Groups[0].Category != "tools" {
		t.Fatalf("appliance groups = %+v, want just the tools group (media empties out)", appliance.Groups)
	}
	if got := ids(appliance.Groups[0].Apps); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("tools group apps = %v, want [alpha gamma]", got)
	}

	hosted, err := syncedCatalogWithHome(t, segApps(), home, "hosted").Home()
	if err != nil {
		t.Fatal(err)
	}
	if hosted.Spotlight == nil || hosted.Spotlight.ID != "beta" {
		t.Fatalf("hosted spotlight = %+v, want beta", hosted.Spotlight)
	}
	if len(hosted.Groups) != 2 {
		t.Fatalf("hosted groups = %+v, want both groups (beta is advertised on hosted)", hosted.Groups)
	}
	if got := ids(hosted.Groups[1].Apps); !reflect.DeepEqual(got, []string{"beta"}) {
		t.Fatalf("hosted media group apps = %v, want [beta]", got)
	}
}

// TestHomeNoBlockYieldsNoSpotlightOrGroups covers a snapshot with no authored
// home block (the box has never synced a landing page, or the sync tool
// published one with an empty home.yml): the landing carries neither a
// spotlight nor groups, while the categories/featured projections it shares
// with the home block are unaffected.
func TestHomeNoBlockYieldsNoSpotlightOrGroups(t *testing.T) {
	h, err := syncedCatalog(t, segApps(), "appliance").Home()
	if err != nil {
		t.Fatal(err)
	}
	if h.Spotlight != nil {
		t.Fatalf("spotlight = %+v, want nil with no home block", h.Spotlight)
	}
	if len(h.Groups) != 0 {
		t.Fatalf("groups = %+v, want none with no home block", h.Groups)
	}
	if want := []string{"media", "tools"}; !reflect.DeepEqual(h.Categories, want) {
		t.Fatalf("categories = %v, want %v (unaffected by the absent home block)", h.Categories, want)
	}
	if got := ids(h.Featured); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("featured = %v, want [alpha] (unaffected by the absent home block)", got)
	}
}

func TestCategoryFiltersAndFolds(t *testing.T) {
	c := syncedCatalog(t, segApps(), "appliance")

	// "tools" on appliance: alpha + gamma, and the featured row rides along.
	tools, err := c.Category("tools")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(tools.Apps); !reflect.DeepEqual(got, []string{"alpha", "gamma"}) {
		t.Fatalf("category tools apps = %v, want [alpha gamma]", got)
	}
	if got := ids(tools.Featured); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("category tools featured = %v, want [alpha]", got)
	}

	// Match is case-insensitive: "Media" resolves the "media" category (alpha only
	// on appliance — beta is hosted-only).
	media, err := c.Category("Media")
	if err != nil {
		t.Fatalf("Category(Media) should fold to media: %v", err)
	}
	if got := ids(media.Apps); !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("category Media apps = %v, want [alpha]", got)
	}

	// An unknown / empty-on-this-surface category is ErrNotFound, not an empty page.
	if _, err := c.Category("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Category(nope) = %v, want ErrNotFound", err)
	}
}

func TestSearchMatchesAndFiltersEnv(t *testing.T) {
	appliance := syncedCatalog(t, segApps(), "appliance")

	// A blank query returns nothing — search narrows, it does not dump the catalog.
	if got, _ := appliance.Search("   "); got != nil {
		t.Fatalf("blank Search = %v, want nil", got)
	}

	// Name match.
	if got, _ := appliance.Search("alpha"); !reflect.DeepEqual(ids(got), []string{"alpha"}) {
		t.Fatalf("Search(alpha) = %v, want [alpha]", ids(got))
	}

	// Category match: "media" hits alpha via its category (gamma is tools-only).
	if got, _ := appliance.Search("MEDIA"); !reflect.DeepEqual(ids(got), []string{"alpha"}) {
		t.Fatalf("Search(MEDIA) = %v, want [alpha] (category match, case-insensitive)", ids(got))
	}

	// Env filter: "beta" is hosted-only, so an appliance search never surfaces it.
	if got, _ := appliance.Search("beta"); got != nil {
		t.Fatalf("appliance Search(beta) = %v, want nil (hosted-only)", ids(got))
	}
	if got, _ := syncedCatalog(t, segApps(), "hosted").Search("beta"); !reflect.DeepEqual(ids(got), []string{"beta"}) {
		t.Fatalf("hosted Search(beta) = %v, want [beta]", ids(got))
	}
}

func TestSegmentedEmptyStoreNoError(t *testing.T) {
	// A never-synced store (disk source with an empty dir stands in for "no apps"):
	// Home is empty but not an error, Category is ErrNotFound, Search is nil.
	c := New(t.TempDir())
	h, err := c.Home()
	if err != nil {
		t.Fatalf("empty Home errored: %v", err)
	}
	if len(h.Categories) != 0 || len(h.Featured) != 0 {
		t.Fatalf("empty Home = %+v, want no categories/featured", h)
	}
	if _, err := c.Category("tools"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Category = %v, want ErrNotFound", err)
	}
	if got, _ := c.Search("x"); got != nil {
		t.Fatalf("empty Search = %v, want nil", got)
	}
}
