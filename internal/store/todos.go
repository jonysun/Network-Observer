package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type TodoRecord struct {
	ID          string     `json:"id"`
	Phase       string     `json:"phase"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	Progress    int        `json:"progress"`
	Blocker     string     `json:"blocker"`
	Source      string     `json:"source"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

var defaultTodos = []TodoRecord{
	{Phase: "1. 数据契约与配置", Title: "观测模型、Agent 配置和 WAL 兼容", Description: "FlowObservation 扩展、ebpf_mode、DNS 开关和断线补传。", Status: "done", Progress: 100, Source: "plan"},
	{Phase: "2. 中心存储与查询", Title: "流量、Presence、DNS 和 MMDB 查询链路", Description: "ClickHouse TTL、方向/端口语义、Presence、DNS 关系和离线 ASN/GeoIP。", Status: "done", Progress: 100, Source: "plan"},
	{Phase: "3. Web 控制台", Title: "实时连接与历史流量可视化", Description: "端口 24 小时上下行、时间序列、持续连接和归属筛选。", Status: "done", Progress: 100, Source: "plan"},
	{Phase: "4. Agent 采集", Title: "DNS UDP/53 元数据采集", Description: "只保留域名、答案 IP、TTL 和时间，不保存 DNS 载荷。", Status: "done", Progress: 100, Source: "plan"},
	{Phase: "4. Agent 采集", Title: "Linux eBPF 内核验收", Description: "在真实 Linux/OpenWrt 上验证 BTF、bpffs、权限、挂载和字节误差。", Status: "in_progress", Progress: 70, Blocker: "当前开发环境是 Windows；需要 Linux/OpenWrt 实机流量测试。", Source: "plan"},
	{Phase: "5. 部署验收", Title: "NAS 中心与单 Agent 闭环测试", Description: "在绿联 NAS 上启动 Compose，验证 TLS/Token、WAL、重启恢复和 Grafana。", Status: "todo", Progress: 0, Source: "plan"},
	{Phase: "5. 部署验收", Title: "10 端点、1 网关、10000 活动连接压力验收", Description: "验证重复观测不双计、采样标记、磁盘水位和 90 天 TTL。", Status: "todo", Progress: 0, Source: "plan"},
}

func validTodoStatus(status string) bool {
	return status == "todo" || status == "in_progress" || status == "done" || status == "blocked"
}
func validTodoPriority(priority string) bool {
	return priority == "low" || priority == "medium" || priority == "high"
}

func (s *Store) EnsureDefaultTodos(ctx context.Context) error {
	var count int
	if err := s.Postgres.QueryRow(ctx, `SELECT count(*) FROM project_todos`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	for _, todo := range defaultTodos {
		id, err := newUUID()
		if err != nil {
			return err
		}
		completedAt := any(nil)
		if todo.Status == "done" {
			completedAt = time.Now().UTC()
		}
		if _, err := s.Postgres.Exec(ctx, `INSERT INTO project_todos (id, phase, title, description, status, priority, progress, blocker, source, completed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, todo.Phase, todo.Title, todo.Description, todo.Status, "medium", todo.Progress, todo.Blocker, todo.Source, completedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListTodos(ctx context.Context, status string) ([]TodoRecord, error) {
	status = strings.TrimSpace(status)
	if status != "" && !validTodoStatus(status) {
		return nil, fmt.Errorf("unsupported todo status %q", status)
	}
	query := `SELECT id::text, phase, title, description, status, priority, progress, blocker, source, completed_at, created_at, updated_at FROM project_todos`
	args := []any{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY CASE status WHEN 'blocked' THEN 0 WHEN 'in_progress' THEN 1 WHEN 'todo' THEN 2 ELSE 3 END, CASE priority WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END, updated_at DESC`
	rows, err := s.Postgres.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TodoRecord, 0)
	for rows.Next() {
		var item TodoRecord
		if err := rows.Scan(&item.ID, &item.Phase, &item.Title, &item.Description, &item.Status, &item.Priority, &item.Progress, &item.Blocker, &item.Source, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateTodo(ctx context.Context, record TodoRecord, actor, requestID string) (TodoRecord, error) {
	if err := validateTodo(record); err != nil {
		return record, err
	}
	id, err := newUUID()
	if err != nil {
		return record, err
	}
	record.ID, record.Source = id, "user"
	if record.Status == "done" {
		record.Progress = 100
		record.CompletedAt = timePtr(time.Now().UTC())
	}
	_, err = s.Postgres.Exec(ctx, `INSERT INTO project_todos (id, phase, title, description, status, priority, progress, blocker, source, completed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'user',$9)`, record.ID, record.Phase, record.Title, record.Description, record.Status, record.Priority, record.Progress, record.Blocker, record.CompletedAt)
	if err != nil {
		return record, err
	}
	if err := s.audit(ctx, actor, "create", "project_todo", record.ID, requestID, nil); err != nil {
		return record, err
	}
	return s.todoByID(ctx, record.ID)
}

func (s *Store) UpdateTodo(ctx context.Context, record TodoRecord, actor, requestID string) (TodoRecord, error) {
	if err := validateTodo(record); err != nil {
		return record, err
	}
	var completedAt any = record.CompletedAt
	if record.Status == "done" {
		record.Progress = 100
		completedAt = time.Now().UTC()
	}
	if record.Status != "done" {
		completedAt = nil
	}
	result, err := s.Postgres.Exec(ctx, `UPDATE project_todos SET phase=$2, title=$3, description=$4, status=$5, priority=$6, progress=$7, blocker=$8, completed_at=$9, updated_at=now() WHERE id=$1`, record.ID, record.Phase, record.Title, record.Description, record.Status, record.Priority, record.Progress, record.Blocker, completedAt)
	if err != nil {
		return record, err
	}
	if result.RowsAffected() == 0 {
		return record, fmt.Errorf("project todo not found")
	}
	if err := s.audit(ctx, actor, "update", "project_todo", record.ID, requestID, nil); err != nil {
		return record, err
	}
	return s.todoByID(ctx, record.ID)
}

func (s *Store) DeleteTodo(ctx context.Context, id, actor, requestID string) error {
	result, err := s.Postgres.Exec(ctx, `DELETE FROM project_todos WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("project todo not found")
	}
	return s.audit(ctx, actor, "delete", "project_todo", id, requestID, nil)
}

func (s *Store) todoByID(ctx context.Context, id string) (TodoRecord, error) {
	var item TodoRecord
	err := s.Postgres.QueryRow(ctx, `SELECT id::text, phase, title, description, status, priority, progress, blocker, source, completed_at, created_at, updated_at FROM project_todos WHERE id=$1`, id).Scan(&item.ID, &item.Phase, &item.Title, &item.Description, &item.Status, &item.Priority, &item.Progress, &item.Blocker, &item.Source, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func validateTodo(record TodoRecord) error {
	if strings.TrimSpace(record.Title) == "" || len(record.Title) > 200 {
		return fmt.Errorf("todo title is required and must be at most 200 characters")
	}
	if strings.TrimSpace(record.Phase) == "" {
		record.Phase = "未分类"
	}
	if len(record.Phase) > 80 || len(record.Description) > 4000 || len(record.Blocker) > 1000 {
		return fmt.Errorf("todo text is too long")
	}
	if !validTodoStatus(record.Status) {
		return fmt.Errorf("invalid todo status")
	}
	if !validTodoPriority(record.Priority) {
		return fmt.Errorf("invalid todo priority")
	}
	if record.Progress < 0 || record.Progress > 100 {
		return fmt.Errorf("todo progress must be between 0 and 100")
	}
	return nil
}

func timePtr(value time.Time) *time.Time { return &value }
