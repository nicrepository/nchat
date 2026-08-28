package domain

import "fmt"

// The dashboard's operational counters (issue #581).
//
// Every metric below is declared, not discovered: it has a key, a sentence
// saying exactly what it counts, an explicit time window, and a unit. That is
// the whole point of the table — a number on an operational dashboard whose
// definition nobody can state is a number nobody can act on, and "usuários
// ativos" means three different things depending on who is asked.
//
// Two things this file deliberately does not contain: a metric the platform
// cannot compute from a cheap aggregate, and an estimate. A counter the query
// could not produce is reported as unavailable, never as zero — the two look
// identical on a card and mean opposite things.

// MetricKey is the stable identifier of one dashboard counter.
type MetricKey string

const (
	MetricUsersTotal      MetricKey = "users.total"
	MetricUsersActiveNow  MetricKey = "users.active_now"
	MetricUsersActive24h  MetricKey = "users.active_24h"
	MetricChannelsActive  MetricKey = "channels.active"
	MetricGroupsActive    MetricKey = "conversations.groups_active"
	MetricDirectActive    MetricKey = "conversations.direct_active"
	MetricMessages24h     MetricKey = "messages.last_24h"
	MetricCallsActive     MetricKey = "calls.active"
	MetricUploads24h      MetricKey = "uploads.last_24h"
	MetricFilesBlocked24h MetricKey = "files.blocked_24h"
	MetricLinksBlocked24h MetricKey = "links.blocked_24h"
	MetricStorageBytes    MetricKey = "storage.stored_bytes"
)

// MetricWindow is the period a counter covers. A closed set, because "since
// when" is the difference between a useful number and a misleading one.
type MetricWindow string

const (
	// MetricWindowInstant is a count of what is true at collection time.
	MetricWindowInstant MetricWindow = "instant"
	// MetricWindowLast24h counts rows created inside the trailing 24 hours.
	MetricWindowLast24h MetricWindow = "last_24h"
	// MetricWindowCumulative counts everything the platform still holds.
	MetricWindowCumulative MetricWindow = "cumulative"
)

// MetricUnit is how a value should be rendered. Closed, so the console never
// has to guess whether a number is a count or a size.
type MetricUnit string

const (
	MetricUnitCount MetricUnit = "count"
	MetricUnitBytes MetricUnit = "bytes"
)

// MetricDefinition is one declared counter.
type MetricDefinition struct {
	Key   MetricKey
	Label string
	// Definition states exactly what is counted, in the terms an operator can
	// verify. It is rendered next to the number rather than hidden in code.
	Definition string
	Window     MetricWindow
	Unit       MetricUnit
	// Capacity reports whether the platform knows a ceiling for this value.
	// Only ever true where a trustworthy limit exists; storage has none, so it
	// reports what is stored and never a percentage of an invented total.
	Capacity bool
}

// PlatformMetric is one counter together with what the platform observed.
//
// Available is separate from Value and is the reason this is not a plain
// map[MetricKey]int64: a failed aggregate must not arrive as zero.
type PlatformMetric struct {
	Definition MetricDefinition
	Value      int64
	Available  bool
}

// PlatformCounters is the raw result of the single aggregate query.
//
// Field-per-metric rather than a map so the store and this package cannot
// disagree about a key: a counter added to one and not the other does not
// compile.
type PlatformCounters struct {
	UsersTotal      int64
	UsersActiveNow  int64
	UsersActive24h  int64
	ChannelsActive  int64
	GroupsActive    int64
	DirectActive    int64
	Messages24h     int64
	CallsActive     int64
	Uploads24h      int64
	FilesBlocked24h int64
	LinksBlocked24h int64
	StorageBytes    int64
}

// metricDefinitions is the declared table, in the order the dashboard renders.
func metricDefinitions() []MetricDefinition {
	return []MetricDefinition{
		{
			Key: MetricUsersActiveNow, Label: "Usuários ativos agora",
			Definition: "Contas distintas com ao menos uma sessão de chat viva: não revogada e ainda dentro dos prazos de inatividade e absoluto.",
			Window:     MetricWindowInstant, Unit: MetricUnitCount,
		},
		{
			Key: MetricUsersActive24h, Label: "Usuários ativos em 24 h",
			Definition: "Contas distintas cuja sessão de chat registrou uso nas últimas 24 horas, incluindo sessões já encerradas.",
			Window:     MetricWindowLast24h, Unit: MetricUnitCount,
		},
		{
			Key: MetricUsersTotal, Label: "Usuários",
			Definition: "Contas existentes que não foram excluídas. Inclui contas suspensas e ainda não ativadas.",
			Window:     MetricWindowCumulative, Unit: MetricUnitCount,
		},
		{
			Key: MetricChannelsActive, Label: "Canais",
			Definition: "Canais com situação ativa, públicos e privados. Canais arquivados não entram.",
			Window:     MetricWindowCumulative, Unit: MetricUnitCount,
		},
		{
			Key: MetricGroupsActive, Label: "Grupos",
			Definition: "Conversas de grupo ativas. Nenhum conteúdo e nenhum membro é lido para produzir este número.",
			Window:     MetricWindowCumulative, Unit: MetricUnitCount,
		},
		{
			Key: MetricDirectActive, Label: "Conversas diretas",
			Definition: "Conversas 1:1 ativas. Nenhum conteúdo e nenhum membro é lido para produzir este número.",
			Window:     MetricWindowCumulative, Unit: MetricUnitCount,
		},
		{
			Key: MetricMessages24h, Label: "Mensagens em 24 h",
			Definition: "Mensagens criadas nas últimas 24 horas, em canais e conversas, incluindo as posteriormente apagadas. Nenhum corpo de mensagem é lido.",
			Window:     MetricWindowLast24h, Unit: MetricUnitCount,
		},
		{
			Key: MetricCallsActive, Label: "Chamadas em curso",
			Definition: "Chamadas tocando ou em andamento neste instante.",
			Window:     MetricWindowInstant, Unit: MetricUnitCount,
		},
		{
			Key: MetricUploads24h, Label: "Uploads em 24 h",
			Definition: "Anexos criados nas últimas 24 horas e ainda não excluídos. Nenhum nome de arquivo é lido.",
			Window:     MetricWindowLast24h, Unit: MetricUnitCount,
		},
		{
			Key: MetricFilesBlocked24h, Label: "Arquivos bloqueados em 24 h",
			Definition: "Anexos que o antimalware recusou nas últimas 24 horas, contados pelo instante da recusa.",
			Window:     MetricWindowLast24h, Unit: MetricUnitCount,
		},
		{
			Key: MetricLinksBlocked24h, Label: "Links bloqueados em 24 h",
			Definition: "Mensagens retidas nas últimas 24 horas por veredito malicioso do Link Scan. Nenhuma URL é lida ou exibida.",
			Window:     MetricWindowLast24h, Unit: MetricUnitCount,
		},
		{
			Key: MetricStorageBytes, Label: "Armazenamento utilizado",
			Definition: "Soma do tamanho cifrado dos anexos vivos. É o que a plataforma guarda, não a capacidade do volume — o backend de armazenamento não informa um total confiável, então nenhum percentual é exibido.",
			Window:     MetricWindowCumulative, Unit: MetricUnitBytes,
			Capacity: false,
		},
	}
}

// counterReaders maps each declared metric onto the field that carries it.
//
// A lookup table rather than a switch, so PlatformMetrics stays a loop over
// the definitions and adding a counter is two lines in one place instead of a
// branch in another.
var counterReaders = map[MetricKey]func(PlatformCounters) int64{
	MetricUsersTotal:      func(c PlatformCounters) int64 { return c.UsersTotal },
	MetricUsersActiveNow:  func(c PlatformCounters) int64 { return c.UsersActiveNow },
	MetricUsersActive24h:  func(c PlatformCounters) int64 { return c.UsersActive24h },
	MetricChannelsActive:  func(c PlatformCounters) int64 { return c.ChannelsActive },
	MetricGroupsActive:    func(c PlatformCounters) int64 { return c.GroupsActive },
	MetricDirectActive:    func(c PlatformCounters) int64 { return c.DirectActive },
	MetricMessages24h:     func(c PlatformCounters) int64 { return c.Messages24h },
	MetricCallsActive:     func(c PlatformCounters) int64 { return c.CallsActive },
	MetricUploads24h:      func(c PlatformCounters) int64 { return c.Uploads24h },
	MetricFilesBlocked24h: func(c PlatformCounters) int64 { return c.FilesBlocked24h },
	MetricLinksBlocked24h: func(c PlatformCounters) int64 { return c.LinksBlocked24h },
	MetricStorageBytes:    func(c PlatformCounters) int64 { return c.StorageBytes },
}

// MetricDefinitions returns the declared table, in render order.
func MetricDefinitions() []MetricDefinition { return metricDefinitions() }

// PlatformMetrics pairs every declared metric with an observed value.
//
// `available` is the whole answer for the whole set on purpose: the counters
// come from one query, so either it ran or it did not, and reporting some
// cards as real and others as missing would be describing a state that cannot
// happen.
func PlatformMetrics(counters PlatformCounters, available bool) []PlatformMetric {
	definitions := metricDefinitions()
	metrics := make([]PlatformMetric, 0, len(definitions))
	for _, definition := range definitions {
		metric := PlatformMetric{Definition: definition, Available: available}
		if read, ok := counterReaders[definition.Key]; ok && available {
			metric.Value = read(counters)
		}
		metrics = append(metrics, metric)
	}
	return metrics
}

// ValidateMetricDefinitions checks that every declared metric can actually be
// filled and says what it counts.
//
// Called from a test for the same reason the other registries are: the table is
// a literal, so a metric declared without a reader is a card that would render
// a permanent zero, and that is a source defect rather than a runtime state.
func ValidateMetricDefinitions() error {
	seen := make(map[MetricKey]struct{})
	for _, definition := range metricDefinitions() {
		if _, duplicate := seen[definition.Key]; duplicate {
			return fmt.Errorf("metric registry: duplicate key %s", definition.Key)
		}
		seen[definition.Key] = struct{}{}
		if err := validateMetricDefinition(definition); err != nil {
			return err
		}
	}
	return nil
}

// validateMetricDefinition checks what one card owes: prose an operator can
// verify, a window and a unit, and a counter that can actually fill it.
func validateMetricDefinition(definition MetricDefinition) error {
	if definition.Label == "" || definition.Definition == "" {
		return fmt.Errorf("metric registry: %s has no label or definition", definition.Key)
	}
	if definition.Window == "" || definition.Unit == "" {
		return fmt.Errorf("metric registry: %s has no window or unit", definition.Key)
	}
	if _, ok := counterReaders[definition.Key]; !ok {
		return fmt.Errorf("metric registry: %s has no counter behind it", definition.Key)
	}
	return nil
}
