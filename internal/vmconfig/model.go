// Package vmconfig converts between the Proxmox config map the ProxCenter API
// returns and the provider's VM model, and computes the changes to apply.
package vmconfig

// Disk is a data disk, identified by its slot (scsi0, virtio1...).
type Disk struct {
	Slot    string
	Storage string
	SizeGB  int64
}

// NIC is a network card, identified by its slot (net0...). Empty strings and
// nil pointers mean "not described": the current value is kept.
type NIC struct {
	Slot     string
	Model    string
	MAC      string
	Bridge   string
	VLAN     *int64
	Firewall *bool
}

// CloudInit holds the cloud-init settings the provider manages. An empty
// field (nil SSHKeys) is not described and keeps its current value; an empty
// IP leaves ipconfig0 to the API (an IPAM VNet allocates the address).
type CloudInit struct {
	User         string
	SSHKeys      []string
	IP           string
	Gateway      string
	Nameserver   string
	SearchDomain string
}

// VM is the part of a QEMU config the provider describes.
type VM struct {
	Name      string
	Cores     int64
	Sockets   int64
	MemoryMB  int64
	CPU       string
	Disks     []Disk
	NICs      []NIC
	CloudInit *CloudInit
}

// Resize grows a disk to an absolute size.
type Resize struct {
	Disk string
	Size string
}

// Move moves a disk to another storage.
type Move struct {
	Disk    string
	Storage string
}

// Changes is what turns one VM into another.
type Changes struct {
	Keys    map[string]any
	Resizes []Resize
	Moves   []Move
}

// Empty tells whether there is nothing to apply.
func (c Changes) Empty() bool {
	return len(c.Keys) == 0 && len(c.Resizes) == 0 && len(c.Moves) == 0
}
