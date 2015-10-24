package namesys

import (
	"fmt"
	"sync"
	"time"

	proto "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/gogo/protobuf/proto"
	mh "github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-multihash"
	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"

	key "github.com/ipfs/go-ipfs/blocks/key"
	pb "github.com/ipfs/go-ipfs/namesys/pb"
	path "github.com/ipfs/go-ipfs/path"
	routing "github.com/ipfs/go-ipfs/routing"
	logging "github.com/ipfs/go-ipfs/vendor/QmXJkcEXB6C9h6Ytb6rrUTFU56Ro62zxgrbxTT3dgjQGA8/go-log"
)

var log = logging.Logger("namesys")

// routingResolver implements NSResolver for the main IPFS SFS-like naming
type routingResolver struct {
	routing routing.IpfsRouting

	cache     map[string]cacheEntry
	cachelock sync.Mutex
	cachelife time.Duration
}

func (r *routingResolver) cacheGet(name string) (path.Path, bool) {
	r.cachelock.Lock()
	entry, ok := r.cache[name]
	r.cachelock.Unlock()
	if ok && time.Now().Sub(entry.recvd) < r.cachelife {
		return entry.val, true
	}

	return "", false
}

func (r *routingResolver) cacheSet(name string, val path.Path) {
	r.cachelock.Lock()
	r.cache[name] = cacheEntry{
		val:   val,
		recvd: time.Now(),
	}
	r.cachelock.Unlock()
}

type cacheEntry struct {
	val   path.Path
	recvd time.Time
}

// NewRoutingResolver constructs a name resolver using the IPFS Routing system
// to implement SFS-like naming on top.
func NewRoutingResolver(route routing.IpfsRouting, cachelife time.Duration) *routingResolver {
	if route == nil {
		panic("attempt to create resolver with nil routing system")
	}

	return &routingResolver{
		routing:   route,
		cache:     make(map[string]cacheEntry),
		cachelife: cachelife,
	}
}

// Resolve implements Resolver.
func (r *routingResolver) Resolve(ctx context.Context, name string) (path.Path, error) {
	return r.ResolveN(ctx, name, DefaultDepthLimit)
}

// ResolveN implements Resolver.
func (r *routingResolver) ResolveN(ctx context.Context, name string, depth int) (path.Path, error) {
	return resolve(ctx, r, name, depth, "/ipns/")
}

// resolveOnce implements resolver. Uses the IPFS routing system to
// resolve SFS-like names.
func (r *routingResolver) resolveOnce(ctx context.Context, name string) (path.Path, error) {
	log.Debugf("RoutingResolve: '%s'", name)
	cached, ok := r.cacheGet(name)
	if ok {
		return cached, nil
	}

	hash, err := mh.FromB58String(name)
	if err != nil {
		log.Warning("RoutingResolve: bad input hash: [%s]\n", name)
		return "", err
	}
	// name should be a multihash. if it isn't, error out here.

	// use the routing system to get the name.
	// /ipns/<name>
	h := []byte("/ipns/" + string(hash))

	ipnsKey := key.Key(h)
	val, err := r.routing.GetValue(ctx, ipnsKey)
	if err != nil {
		log.Warning("RoutingResolve get failed.")
		return "", err
	}

	entry := new(pb.IpnsEntry)
	err = proto.Unmarshal(val, entry)
	if err != nil {
		return "", err
	}

	// name should be a public key retrievable from ipfs
	pubkey, err := routing.GetPublicKey(r.routing, ctx, hash)
	if err != nil {
		return "", err
	}

	hsh, _ := pubkey.Hash()
	log.Debugf("pk hash = %s", key.Key(hsh))

	// check sig with pk
	if ok, err := pubkey.Verify(ipnsEntryDataForSig(entry), entry.GetSignature()); err != nil || !ok {
		return "", fmt.Errorf("Invalid value. Not signed by PrivateKey corresponding to %v", pubkey)
	}

	// ok sig checks out. this is a valid name.

	// check for old style record:
	valh, err := mh.Cast(entry.GetValue())
	if err != nil {
		// Not a multihash, probably a new record
		p, err := path.ParsePath(string(entry.GetValue()))
		if err != nil {
			return "", err
		}
		r.cacheSet(name, p)
		return p, nil
	} else {
		// Its an old style multihash record
		log.Warning("Detected old style multihash record")
		p := path.FromKey(key.Key(valh))
		r.cacheSet(name, p)
		return p, nil
	}
}
