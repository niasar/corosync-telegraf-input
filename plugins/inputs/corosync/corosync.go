package corosync

import (
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

var execCommand = exec.Command

type Corosync struct {
	Log            telegraf.Logger
	quorumToolPath string
	cfgToolPath    string
	useSudo        bool `toml:"use_sudo"`
	sudoPath       string
}

type ringStatus struct {
	address   string
	status    string
	protocol  string
	ringId    uint64
	linkCount [5]uint32
}

type votequorumStatus struct {
	totalVotes      uint
	expectedVotes   uint
	highestExpected uint
	quorum          uint
	flags           []string
}

type quorumStatus struct {
	nodeId     uint
	ringId     string
	isQuorate  bool
	totalNodes uint
}

type nodeStatus struct {
	quorum     quorumStatus
	votequorum votequorumStatus
	rings      []ringStatus
}

func (c *Corosync) Description() string {
	return "Gather status of Corosync Cluster Engine"
}

func (c *Corosync) SampleConfig() string {
	return sampleConfig
}

func combinedOutputTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	type result struct {
		output []byte
		err    error
	}
	resultChan := make(chan result, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		resultChan <- result{output: out, err: err}
	}()
	select {
	case <-time.After(timeout):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return []byte{}, errors.New("timeout")
	case ret := <-resultChan:
		return ret.output, ret.err
	}
}

func (c *Corosync) Init() error {
	var err error
	c.cfgToolPath, err = exec.LookPath("corosync-cfgtool")
	if err != nil {
		return fmt.Errorf("unable to locate corosync-cfgtool in PATH: %w", err)
	}
	c.quorumToolPath, err = exec.LookPath("corosync-quorumtool")
	if err != nil {
		return fmt.Errorf("unable to locate corosync-quorumtool in PATH: %w", err)
	}
	if c.useSudo {
		c.sudoPath, err = exec.LookPath("sudo")
		if err != nil {
			return fmt.Errorf("unable to locate sudo in PATH: %w", err)
		}
	}
	return nil
}

func (c *Corosync) Gather(acc telegraf.Accumulator) error {
	status := nodeStatus{rings: []ringStatus{}}
	var quorumToolCmd *exec.Cmd
	var cfgToolCmd *exec.Cmd
	if !c.useSudo {
		quorumToolCmd = execCommand(c.quorumToolPath)
		cfgToolCmd = execCommand(c.cfgToolPath, "-sb")
	} else {
		quorumToolCmd = execCommand(c.sudoPath, c.quorumToolPath)
		cfgToolCmd = execCommand(c.sudoPath, c.cfgToolPath, "-sb")
	}
	quorumToolOut, err := combinedOutputTimeout(quorumToolCmd, time.Second*5)
	if err != nil {
		return fmt.Errorf("command %q failed: %w", quorumToolCmd, err)
	}
	cfgToolOut, err := combinedOutputTimeout(cfgToolCmd, time.Second*5)
	if err != nil {
		return fmt.Errorf("corosync-cfgtool encountered an error: %w", err)
	}
	err = status.quorum.parseQuorumStatus(quorumToolOut)
	if err != nil {
		return fmt.Errorf("unable to parse corosync-quorumtool Quorum output: %w", err)
	}
	err = status.votequorum.parseVotequorumInfo(quorumToolOut)
	if err != nil {
		return fmt.Errorf("unable to parse corosync-quorumtool Votequorum output: %w", err)
	}
	err = status.parseRingStatus(cfgToolOut)
	if err != nil {
		return fmt.Errorf("unable to parse corosync-cfgtool output: %w", err)
	}
	acc.AddGauge("corosync_quorum", map[string]interface{}{
		"is_quorate":       status.quorum.isQuorate,
		"total_nodes":      status.quorum.totalNodes,
		"ring_id":          status.quorum.ringId,
		"total_votes":      status.votequorum.totalVotes,
		"expected_votes":   status.votequorum.expectedVotes,
		"highest_expected": status.votequorum.highestExpected,
		"quorum":           status.votequorum.quorum,
	}, map[string]string{
		"node_id": fmt.Sprintf("%d", status.quorum.nodeId),
	})
	for _, ring := range status.rings {
		acc.AddGauge("corosync_rings", map[string]interface{}{
			"active":    ring.linkCount[0],
			"connected": ring.linkCount[1],
			"enabled":   ring.linkCount[2],
			"unknown":   ring.linkCount[3],
			"undefined": ring.linkCount[4],
			"total":     len(ring.status) - 1,
		}, map[string]string{
			"ring_id": fmt.Sprint(ring.ringId),
		})
	}
	return nil
}

// Parses quorum section of corosync-quorumtool output
func (s *quorumStatus) parseQuorumStatus(output []byte) error {
	re := regexp.MustCompile(`Date:\s+([\w\n :]+)\nQuorum provider:\s+(\w+)\nNodes:\s+(\d+)\nNode ID:\s+(\d+)\nRing ID:\s+([\w\d\.]+)\nQuorate:\s+(\w+)`)
	matches := re.FindAllSubmatch(output, -1)
	if matches == nil {
		return errors.New("corosync-quorumtool returned incorrect data")
	}
	total, err := strconv.ParseUint(string(matches[0][3]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse total nodes into uint: %w", err)
	}
	s.totalNodes = uint(total)
	nodeId, err := strconv.ParseUint(string(matches[0][4]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse Node ID: %w", err)
	}
	s.nodeId = uint(nodeId)
	s.ringId = string(matches[0][5])
	switch string(matches[0][6]) {
	case "Yes":
		s.isQuorate = true
	case "No":
		s.isQuorate = false
	default:
		return errors.New("unable to parse quorum status")
	}
	return nil
}

// Parses rings status from corosync-cfgtool output
func (s *nodeStatus) parseRingStatus(output []byte) error {
	ringsRE := regexp.MustCompile(`LINK ID (?P<id>\d+) (?P<proto>\w+)\s+addr\s+= (?P<address>.+)\s+status\s+= (?P<status>.+)`)
	for _, match := range ringsRE.FindAllSubmatch(output, -1) {
		namedMatches := extractCapturedGroups(ringsRE, match)
		id, err := strconv.ParseUint(namedMatches["id"], 10, 64)
		if err != nil {
			return fmt.Errorf("unable to parse Ring ID: %w", err)
		}
		s.rings = append(s.rings, ringStatus{
			ringId:    id,
			protocol:  namedMatches["proto"],
			address:   namedMatches["address"],
			status:    namedMatches["status"],
			linkCount: countLinks(namedMatches["status"]),
		})
	}
	return nil
}

// Parses Votequorum info from corosync-quorumtool output
func (s *votequorumStatus) parseVotequorumInfo(output []byte) error {
	voteqorumInfoRE := regexp.MustCompile(`Expected votes:\s+(\d+)\s*\nHighest expected:\s+(\d+)\s*\nTotal votes:\s+(\d+)\s*\nQuorum:\s+(\d+)\s*\nFlags:\s+([\w]+)`)
	votequorumInfoMatch := voteqorumInfoRE.FindAllSubmatch(output, -1)
	expected, err := strconv.ParseUint(string(votequorumInfoMatch[0][1]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse expected votes into uint: %w", err)
	}
	highest, err := strconv.ParseUint(string(votequorumInfoMatch[0][2]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse highest expected into uint: %w", err)
	}
	total, err := strconv.ParseUint(string(votequorumInfoMatch[0][3]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse total votes into uint: %w", err)
	}
	quorum, err := strconv.ParseUint(string(votequorumInfoMatch[0][4]), 10, 32)
	if err != nil {
		return fmt.Errorf("unable to parse quorum into uint: %w", err)
	}
	s.expectedVotes = uint(expected)
	s.highestExpected = uint(highest)
	s.totalVotes = uint(total)
	s.quorum = uint(quorum)
	s.flags = strings.Split(string(votequorumInfoMatch[0][5]), " ")
	return nil
}

// Extract named captured groups into map from RE match
func extractCapturedGroups(re *regexp.Regexp, match [][]byte) map[string]string {
	namedMatches := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			namedMatches[name] = string(match[i])
		}
	}
	return namedMatches
}

// Counts links in the ring by status from corosync-cfgtool output (localhost not included)
func countLinks(status string) [5]uint32 {
	var result [5]uint32
	for _, char := range status {
		switch char {
		case '3': // active
			result[0] += 1
		case '2': // connected
			result[1] += 1
		case '1': // enabled
			result[2] += 1
		case '?': // unknown
			result[3] += 1
		case 'n': // localhost
			continue
		default:
			result[4] += 1
		}
	}
	return result
}

func init() {
	inputs.Add("corosync", func() telegraf.Input {
		return &Corosync{
			useSudo: true,
		}
	})
}
