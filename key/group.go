package key

// Group is a list of Public keys providing helper methods to search and

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
	kyber "github.com/drand/kyber"
	dkg "github.com/drand/kyber/share/dkg"
	"golang.org/x/crypto/blake2b"

	proto "github.com/drand/drand/protobuf/drand"
)

// XXX new256 returns an error so we make a wrapper around
var hashFunc = func() hash.Hash { h, _ := blake2b.New256(nil); return h }

// Group holds all information about a group of drand nodes.
type Group struct {
	// Threshold to setup during the DKG or resharing protocol.
	Threshold int
	// Period to use for the beacon randomness generation
	Period time.Duration
	// List of nodes forming this group
	Nodes []*Node
	// Time at which the first round of the chain is mined
	GenesisTime int64
	// Seed of the genesis block. When doing a DKG from scratch, it will be
	// populated directly from the list of nodes and other parameters. WHen
	// doing a resharing, this seed is taken from the first group of the
	// network.
	GenesisSeed []byte
	// In case of a resharing, this is the time at which the network will
	// transition from the old network to the new network.
	TransitionTime int64
	// The distributed public key of this group. It is nil if the group has not
	// ran a DKG protocol yet.
	PublicKey *DistPublic
}

// Contains returns the Node that is equal to the given identity (without the
// index). If the node is not found, Find returns nil.
func (g *Group) Find(pub *Identity) *Node {
	for _, pu := range g.Nodes {
		if pu.Identity.Equal(pub) {
			return pu
		}
	}
	return nil
}

// Node returns the node at the given index if it exists in the group. If it does
// not, Node() returns nil.
func (g *Group) Node(i Index) *Node {
	for _, n := range g.Nodes {
		if n.Index == i {
			return n
		}
	}
	return nil
}

// DKGNodes return the slice of nodes of this group that is consumable by the
// dkg library: only the public key and index are used.
func (g *Group) DKGNodes() []dkg.Node {
	dnodes := make([]dkg.Node, len(g.Nodes))
	for i, node := range g.Nodes {
		dnodes[i] = dkg.Node{
			Index:  node.Index,
			Public: node.Identity.Key,
		}
	}
	return dnodes
}

func (g *Group) Hash() []byte {
	h := hashFunc()
	sort.Slice(g.Nodes, func(i, j int) bool {
		return g.Nodes[i].Index < g.Nodes[j].Index
	})
	// all nodes public keys and positions
	for _, n := range g.Nodes {
		h.Write(n.Hash())
	}
	binary.Write(h, binary.LittleEndian, uint32(g.Threshold))
	binary.Write(h, binary.LittleEndian, uint64(g.GenesisTime))
	if g.TransitionTime != 0 {
		binary.Write(h, binary.LittleEndian, g.TransitionTime)
	}
	if g.PublicKey != nil {
		h.Write(g.PublicKey.Hash())
	}
	return h.Sum(nil)
}

func (g *Group) identities() []*Identity {
	ids := make([]*Identity, g.Len())
	for i := 0; i < g.Len(); i++ {
		ids[i] = g.Nodes[i].Identity
	}
	return ids
}

// Points returns itself under the form of a list of kyber.Point
func (g *Group) Points() []kyber.Point {
	pts := make([]kyber.Point, g.Len())
	for i, pu := range g.Nodes {
		pts[i] = pu.Key
	}
	return pts
}

// Len returns the number of participants in the group
func (g *Group) Len() int {
	return len(g.Nodes)
}

func (g *Group) String() string {
	var b bytes.Buffer
	toml.NewEncoder(&b).Encode(g.TOML())
	return b.String()
}

func (g *Group) Equal(g2 *Group) bool {
	if g.Threshold != g2.Threshold {
		return false
	}
	if g.Period.String() != g2.Period.String() {
		return false
	}
	if g.Len() != g2.Len() {
		return false
	}
	if !bytes.Equal(g.GetGenesisSeed(), g2.GetGenesisSeed()) {
		return false
	}
	if g.TransitionTime != g2.TransitionTime {
		return false
	}
	for i := 0; i < g.Len(); i++ {
		if !g.Nodes[i].Equal(g2.Nodes[i]) {
			return false
		}
	}
	if g.PublicKey != nil {
		if g2.PublicKey != nil {
			// both keys aren't nil so we verify
			return g.PublicKey.Equal(g2.PublicKey)
		} else {
			// g is not nil g2 is nil
			return false
		}
	} else if g2.PublicKey != nil {
		// g is nil g2 is not nil
		return false
	}
	return true
}

// GroupTOML is the representation of a Group TOML compatible
type GroupTOML struct {
	Threshold      int
	Period         string
	Nodes          []*NodeTOML
	GenesisTime    int64
	TransitionTime int64           `toml:omitempty`
	GenesisSeed    string          `toml:omitempty`
	PublicKey      *DistPublicTOML `toml:omitempty`
}

// FromTOML decodes the group from the toml struct
func (g *Group) FromTOML(i interface{}) (err error) {
	gt, ok := i.(*GroupTOML)
	if !ok {
		return fmt.Errorf("grouptoml unknown")
	}
	g.Threshold = gt.Threshold
	g.Nodes = make([]*Node, len(gt.Nodes))
	for i, ptoml := range gt.Nodes {
		g.Nodes[i] = new(Node)
		if err := g.Nodes[i].FromTOML(ptoml); err != nil {
			return fmt.Errorf("group: unwrapping node[%d]: %v", i, err)
		}
	}

	if g.Threshold < dkg.MinimumT(len(gt.Nodes)) {
		return errors.New("group file have threshold 0")
	} else if g.Threshold > g.Len() {
		return errors.New("group file threshold greater than number of participants")
	}

	if gt.PublicKey != nil {
		// dist key only if dkg ran
		g.PublicKey = &DistPublic{}
		if err = g.PublicKey.FromTOML(gt.PublicKey); err != nil {
			return fmt.Errorf("group: unwrapping distributed public key: %v", err)
		}
	}
	g.Period, err = time.ParseDuration(gt.Period)
	if err != nil {
		return err
	}
	g.GenesisTime = gt.GenesisTime
	if gt.TransitionTime != 0 {
		g.TransitionTime = gt.TransitionTime
	}
	if gt.GenesisSeed != "" {
		if g.GenesisSeed, err = hex.DecodeString(gt.GenesisSeed); err != nil {
			return fmt.Errorf("group: decoding genesis seed %v", err)
		}
	}
	return nil
}

// TOML returns a TOML-encodable version of the Group
func (g *Group) TOML() interface{} {
	gtoml := &GroupTOML{
		Threshold: g.Threshold,
	}
	gtoml.Nodes = make([]*NodeTOML, g.Len())
	for i, n := range g.Nodes {
		gtoml.Nodes[i] = n.TOML().(*NodeTOML)
	}

	if g.PublicKey != nil {
		gtoml.PublicKey = g.PublicKey.TOML().(*DistPublicTOML)
	}
	gtoml.Period = g.Period.String()
	gtoml.GenesisTime = g.GenesisTime
	if g.TransitionTime != 0 {
		gtoml.TransitionTime = g.TransitionTime
	}
	gtoml.GenesisSeed = hex.EncodeToString(g.GetGenesisSeed())
	return gtoml
}

// GetGenesisSeed exposes the hash of the genesis seed for the group
func (g *Group) GetGenesisSeed() []byte {
	if g.GenesisSeed != nil {
		return g.GenesisSeed
	}

	g.GenesisSeed = g.Hash()
	return g.GenesisSeed
}

// TOMLValue returns an empty TOML-compatible value of the group
func (g *Group) TOMLValue() interface{} {
	return &GroupTOML{}
}

// NewGroup returns a group from the given information to be used as a new group
// in a setup or resharing phase. Every identity is map to a Node struct whose
// index is the position in the list of identity.
func NewGroup(list []*Identity, threshold int, genesis int64, period time.Duration) *Group {
	return &Group{
		Nodes:       copyAndSort(list),
		Threshold:   threshold,
		GenesisTime: genesis,
		Period:      period,
	}
}

// LoadGroup returns a group that contains all information with respect
// to a QUALified set of nodes that ran successfully a setup or reshare phase.
// The threshold is automatically guessed from the length of the distributed
// key.
func LoadGroup(list []*Node, genesis int64, public *DistPublic, period time.Duration, transition int64) *Group {
	return &Group{
		Nodes:          list,
		Threshold:      len(public.Coefficients),
		PublicKey:      public,
		Period:         period,
		GenesisTime:    genesis,
		TransitionTime: transition,
	}
}

func copyAndSort(list []*Identity) []*Node {
	nl := make([]*Identity, len(list))
	copy(nl, list)
	sort.Sort(ByKey(nl))
	nodes := make([]*Node, len(list))
	for i := 0; i < len(list); i++ {
		nodes[i] = &Node{
			Identity: nl[i],
			Index:    Index(i),
		}
	}
	return nodes
}

// MinimumT calculates the threshold needed for the group to produce sufficient shares to decode
func MinimumT(n int) int {
	return int(math.Floor(float64(n)/2.0) + 1)
}

// GroupFromProto convertes a protobuf group into a local Group object
func GroupFromProto(g *proto.GroupPacket) (*Group, error) {
	var nodes = make([]*Node, 0, len(g.GetNodes()))
	for _, id := range g.GetNodes() {
		kid, err := NodeFromProto(id)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, kid)
	}
	n := len(nodes)
	thr := int(g.GetThreshold())
	if thr < MinimumT(n) {
		return nil, fmt.Errorf("invalid threshold: %d vs %d (minimum)", thr, MinimumT(n))
	}
	genesisTime := int64(g.GetGenesisTime())
	if genesisTime == 0 {
		return nil, fmt.Errorf("genesis time zero")
	}
	period := time.Duration(g.GetPeriod()) * time.Second
	if period == time.Duration(0) {
		return nil, fmt.Errorf("period time is zero")
	}
	var dist = new(DistPublic)
	for _, coeff := range g.DistKey {
		c := KeyGroup.Point()
		if err := c.UnmarshalBinary(coeff); err != nil {
			return nil, fmt.Errorf("invalid distributed key coefficients:%v", err)
		}
		dist.Coefficients = append(dist.Coefficients, c)
	}
	//group := key.NewGroup(nodes, thr, genesisTime)
	group := new(Group)
	group.Nodes = nodes
	group.Threshold = thr
	group.GenesisTime = genesisTime
	group.Period = period
	group.TransitionTime = int64(g.GetTransitionTime())
	if g.GetGenesisSeed() != nil {
		group.GenesisSeed = g.GetGenesisSeed()
	}
	if len(dist.Coefficients) > 0 {
		if len(dist.Coefficients) != group.Threshold {
			return nil, fmt.Errorf("public coefficient length %d is not equal to threshold %d", len(dist.Coefficients), group.Threshold)
		}
		group.PublicKey = dist
	}
	return group, nil
}

// ToProto encodes a local group object into its wire format
func (g *Group) ToProto() *proto.GroupPacket {
	var out = new(proto.GroupPacket)
	var ids = make([]*proto.Node, len(g.Nodes))
	for i, id := range g.Nodes {
		key, _ := id.Key.MarshalBinary()
		ids[i] = &proto.Node{
			Public: &proto.Identity{
				Address: id.Address(),
				Tls:     id.IsTLS(),
				Key:     key,
			},
			Index: id.Index,
		}
	}
	out.Nodes = ids
	out.Period = uint32(g.Period.Seconds())
	out.Threshold = uint32(g.Threshold)
	out.GenesisTime = uint64(g.GenesisTime)
	out.TransitionTime = uint64(g.TransitionTime)
	out.GenesisSeed = g.GetGenesisSeed()
	if g.PublicKey != nil {
		var coeffs = make([][]byte, len(g.PublicKey.Coefficients))
		for i, c := range g.PublicKey.Coefficients {
			buff, _ := c.MarshalBinary()
			coeffs[i] = buff
		}
		out.DistKey = coeffs
	}
	return out
}
