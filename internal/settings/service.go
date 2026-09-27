package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"megaapp-back/internal/platform/idempotency"
)

const (
	NamespaceCore    = "core"
	NamespaceFood    = "food"
	NamespaceMoney   = "money"
	NamespaceMetrics = "metrics"
)

var ErrInvalidNamespace = errors.New("invalid settings namespace")
var ErrInvalidSettingPayload = errors.New("invalid settings payload")

//                                                                          CORE

type CoreSettings struct {
	SelectedChapterFood  bool `json:"selectedChapterFood"`
	SelectedChapterMoney bool `json:"selectedChapterMoney"`
}

//                                                                          FOOD

type SavedFoodStatsDateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type FoodSettings struct {
	Height                   *int64                   `json:"height"`
	StatsDateRange           *SavedFoodStatsDateRange `json:"statsDateRange"`
	StatsTopProductsMetric   string                   `json:"statsTopProductsMetric"`
	StatsAccordionOpenBlocks []string                 `json:"statsAccordionOpenBlocks"`
}

func defaultFoodSettings() *FoodSettings {
	return &FoodSettings{
		StatsTopProductsMetric:   "kcal",
		StatsAccordionOpenBlocks: []string{"streak", "milestones", "charts", "topProducts"},
	}
}

//                                                                         MONEY

type ExpenseChartYMaxSetting struct {
	RawValue       string `json:"rawValue"`
	CurrencyTicker string `json:"currencyTicker"`
}

type MoneySettings struct {
	DisplayCurrency           string                   `json:"displayCurrency"`
	ChartRangeStart           *string                  `json:"chartRangeStart"`
	ChartRangeEnd             *string                  `json:"chartRangeEnd"`
	ExpenseChartYMax          *ExpenseChartYMaxSetting `json:"expenseChartYMax"`
	ConvertToUnifiedCurrency  bool                     `json:"convertToUnifiedCurrency"`
	DisabledCategoryIds       []*int64                 `json:"disabledCategoryIds"`
	DisabledIncomeCategoryIds []*int64                 `json:"disabledIncomeCategoryIds"`
	YearlyMode                bool                     `json:"yearlyMode"`
	ShowByAccount             bool                     `json:"showByAccount"`
	SuspensionFilter          string                   `json:"suspensionFilter"`
	DisabledAccountIds        []int64                  `json:"disabledAccountIds"`
}

func defaultMoneySettings() *MoneySettings {
	return &MoneySettings{
		DisplayCurrency:           "RUB",
		DisabledCategoryIds:       []*int64{},
		DisabledIncomeCategoryIds: []*int64{},
		SuspensionFilter:          "all",
		DisabledAccountIds:        []int64{},
	}
}

//                                                                        METRICS

type CardSize struct {
	WidthPx          float64 `json:"widthPx"`
	HeightPx         float64 `json:"heightPx"`
	ExpandedHeightPx float64 `json:"expandedHeightPx"`
}

type SeverityThresholds struct {
	WarnAfterSeconds  float64 `json:"warnAfterSeconds"`
	ErrorAfterSeconds float64 `json:"errorAfterSeconds"`
}

type ServiceCustomLabel struct {
	Short string `json:"short"`
	Long  string `json:"long"`
}

// UnmarshalJSON also accepts the pre-existing plain-string shape ({"svc": "Label"}), which is
// what every already-stored row has today — there's real production data in this exact shape.
// A bare string becomes both Short and Long; the next time this namespace is saved it's
// persisted back out in the new {short, long} shape via the default struct marshaling.
func (l *ServiceCustomLabel) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		l.Short = legacy
		l.Long = legacy
		return nil
	}

	type shape ServiceCustomLabel
	var v shape
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*l = ServiceCustomLabel(v)
	return nil
}

type MetricsSettings struct {
	CardSize                  CardSize                      `json:"cardSize"`
	SyncCrosshairEnabled      bool                          `json:"syncCrosshairEnabled"`
	DashboardSelection        map[string]map[string]float64 `json:"dashboardSelection"`
	DashboardServiceSelection map[string]float64            `json:"dashboardServiceSelection"`
	MetricChartModeOverrides  map[string]map[string]string  `json:"metricChartModeOverrides"`
	SeverityThresholds        map[string]SeverityThresholds `json:"severityThresholds"`
	ServiceHeaderVisibility   map[string]bool               `json:"serviceHeaderVisibility"`
	ServiceCustomLabels       map[string]ServiceCustomLabel `json:"serviceCustomLabels"`
	AnomalyCorridorPercent    float64                       `json:"anomalyCorridorPercent"`
}

func defaultMetricsSettings() *MetricsSettings {
	return &MetricsSettings{
		CardSize:                  CardSize{WidthPx: 304, HeightPx: 112, ExpandedHeightPx: 400},
		DashboardSelection:        map[string]map[string]float64{},
		DashboardServiceSelection: map[string]float64{},
		MetricChartModeOverrides:  map[string]map[string]string{},
		SeverityThresholds:        map[string]SeverityThresholds{},
		ServiceHeaderVisibility:   map[string]bool{},
		ServiceCustomLabels:       map[string]ServiceCustomLabel{},
		AnomalyCorridorPercent:    95,
	}
}

//                                                                       REGISTRY

// namespaceDescriptor bundles what the generic Get/Put mechanism needs to know about one
// namespace: its Go shape (for whitelist + round-trip validation) and its default values.
type namespaceDescriptor struct {
	newStruct func() any
	defaults  func() any
}

var namespaceDescriptors = map[string]namespaceDescriptor{
	NamespaceCore: {
		newStruct: func() any { return &CoreSettings{} },
		defaults:  func() any { return &CoreSettings{} },
	},
	NamespaceFood: {
		newStruct: func() any { return &FoodSettings{} },
		defaults:  func() any { return defaultFoodSettings() },
	},
	NamespaceMoney: {
		newStruct: func() any { return &MoneySettings{} },
		defaults:  func() any { return defaultMoneySettings() },
	},
	NamespaceMetrics: {
		newStruct: func() any { return &MetricsSettings{} },
		defaults:  func() any { return defaultMetricsSettings() },
	},
}

func IsValidNamespace(namespace string) bool {
	_, ok := namespaceDescriptors[namespace]
	return ok
}

//                                                                        SERVICE

type Service struct {
	repo        *Repository
	idempotency *idempotency.Store
}

func NewService(repo *Repository, idempotencyStore *idempotency.Store) *Service {
	return &Service{repo: repo, idempotency: idempotencyStore}
}

// Get returns a namespace's settings as JSON, with the stored payload merged onto that
// namespace's defaults (a field never saved before falls back to its default, not null).
func (s *Service) Get(ctx context.Context, userID int64, namespace string) (json.RawMessage, error) {
	descriptor, ok := namespaceDescriptors[namespace]
	if !ok {
		return nil, ErrInvalidNamespace
	}

	stored, err := s.repo.Get(ctx, userID, namespace)
	if err != nil {
		return nil, err
	}

	result := descriptor.defaults()
	if stored != "" {
		if err := json.Unmarshal([]byte(stored), result); err != nil {
			return nil, fmt.Errorf("unmarshal stored %s settings: %w", namespace, err)
		}
	}

	return json.Marshal(result)
}

// GetWithProfile is Get plus, for the core namespace only, the read-only userName/isUserAdmin
// fields. Those live on the users table (identity/authorization, not a setting) and are merged
// in here rather than ever being part of CoreSettings — see the backend plan for why.
func (s *Service) GetWithProfile(ctx context.Context, userID int64, namespace string, fallbackUserName string) (json.RawMessage, error) {
	raw, err := s.Get(ctx, userID, namespace)
	if err != nil {
		return nil, err
	}
	if namespace != NamespaceCore {
		return raw, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("unmarshal core settings: %w", err)
	}

	isAdmin, userName, err := s.repo.GetUserAdminAndName(ctx, userID)
	if err != nil {
		return nil, err
	}
	if userName == "" {
		userName = fallbackUserName
	}

	userNameJSON, err := json.Marshal(userName)
	if err != nil {
		return nil, fmt.Errorf("marshal userName: %w", err)
	}
	isAdminJSON, err := json.Marshal(isAdmin)
	if err != nil {
		return nil, fmt.Errorf("marshal isUserAdmin: %w", err)
	}
	fields["userName"] = userNameJSON
	fields["isUserAdmin"] = isAdminJSON

	return json.Marshal(fields)
}

// Put merges 1..N top-level fields into a namespace, validated against that namespace's struct
// twice: once against exactly the incoming fields (strict — rejects any field name the struct
// doesn't declare, closing off smuggling something like isUserAdmin through the generic merge),
// and once against the full merged payload after merge (permissive on unrelated stored keys —
// a field a namespace no longer declares, e.g. after being reclassified to local-only on the
// frontend, just sits unread — but still strict on type for every field the struct does declare).
// Idempotent via the same operationId mechanism as every other write path (plan 20, Pattern A):
// BeginTx → Find → merge+validate → Record → Commit, all in one transaction.
func (s *Service) Put(ctx context.Context, userID int64, namespace string, operationID string, fields map[string]json.RawMessage) (bool, int64, error) {
	descriptor, ok := namespaceDescriptors[namespace]
	if !ok {
		return false, 0, ErrInvalidNamespace
	}
	if len(fields) == 0 {
		return false, 0, ErrInvalidSettingPayload
	}
	if err := validateWhitelist(descriptor, fields); err != nil {
		return false, 0, err
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return false, 0, err
	}

	if _, found, err := s.idempotency.Find(ctx, tx, userID, operationID); err != nil {
		_ = tx.Rollback()
		return false, 0, err
	} else if found {
		_ = tx.Rollback()
		return false, 0, nil
	}

	merged, updatedAtMillis, err := s.repo.MergeFields(ctx, tx, userID, namespace, fields)
	if err != nil {
		_ = tx.Rollback()
		return false, 0, err
	}
	if err := validateRoundTrip(descriptor, merged); err != nil {
		_ = tx.Rollback()
		return false, 0, err
	}

	if err := s.idempotency.Record(ctx, tx, userID, operationID, "{}"); err != nil {
		_ = tx.Rollback()
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("commit settings update: %w", err)
	}

	return true, updatedAtMillis, nil
}

func validateWhitelist(descriptor namespaceDescriptor, fields map[string]json.RawMessage) error {
	if err := rejectNullForNonPointerFields(descriptor, fields); err != nil {
		return err
	}

	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal input fields: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(descriptor.newStruct()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSettingPayload, err)
	}

	return nil
}

// encoding/json silently ignores a JSON `null` when unmarshaling into a non-pointer field —
// the field is just left at its zero value, no error. That means {"selectedChapterFood":null}
// would otherwise sail through both DisallowUnknownFields() above and the round-trip check below,
// and MergeFields would write the literal JSON null into the stored payload instead of a real
// bool/string/slice. Only fields declared as a pointer in the namespace struct (e.g. `height
// *int64`) are meant to accept null — reject it everywhere else before it ever reaches storage.
func rejectNullForNonPointerFields(descriptor namespaceDescriptor, fields map[string]json.RawMessage) error {
	structType := reflect.TypeOf(descriptor.newStruct()).Elem()
	nonPointerFields := make(map[string]bool, structType.NumField())
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		nonPointerFields[name] = field.Type.Kind() != reflect.Ptr
	}

	for key, raw := range fields {
		if string(raw) == "null" && nonPointerFields[key] {
			return fmt.Errorf("%w: field %q cannot be null", ErrInvalidSettingPayload, key)
		}
	}

	return nil
}

func validateRoundTrip(descriptor namespaceDescriptor, mergedJSON string) error {
	if err := json.Unmarshal([]byte(mergedJSON), descriptor.newStruct()); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSettingPayload, err)
	}
	return nil
}
