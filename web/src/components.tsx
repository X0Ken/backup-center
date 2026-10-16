import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Switch,
  Table,
  Pagination,
  Popover,
  Tag,
  Message,
  Spin,
} from "@arco-design/web-react";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { api, bytes, date } from "./api";
import { actionLabel, actionLabels, kindLabels } from "./types";
echarts.use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  CanvasRenderer,
]);
export type Row = Record<string, any>;
export type Field = {
  key: string;
  label: string;
  value?: any;
  type?: "select" | "number" | "switch" | "password" | "textarea";
  options?: { label: string; value: any }[];
  required?: boolean;
  help?: string | ((values: Row) => string);
  min?: number;
  max?: number;
  visible?: (values: Row) => boolean;
  disabled?: boolean;
};
export function FormDialog({
  title,
  fields,
  onSubmit,
  onClose,
  warning,
}: {
  title: string;
  fields: Field[];
  onSubmit: (v: Row) => Promise<unknown>;
  onClose: () => void;
  warning?: string;
}) {
  const [form] = Form.useForm();
  const initial = Object.fromEntries(
    fields.map((f) => [f.key, f.value ?? (f.type === "switch" ? false : "")]),
  );
  const [values, setValues] = useState<Row>(initial);
  const visibleFields = fields.filter((f) => !f.visible || f.visible(values));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Modal
      title={title}
      visible
      onCancel={() => !busy && onClose()}
      confirmLoading={busy}
      okText={
        title.startsWith("删除")
          ? "确认删除"
          : /创建|新建/.test(title)
            ? "创建"
            : title.includes("清理")
              ? "开始清理"
              : title.includes("对比")
                ? "对比"
                : title.includes("下载")
                  ? "下载"
                  : title.includes("信任")
                    ? "信任主机"
                    : "保存"
      }
      cancelText="取消"
      style={{ width: 620 }}
      onOk={async () => {
        try {
          const values = await form.validate();
          setBusy(true);
          setError("");
          await onSubmit(
            Object.fromEntries(
              visibleFields.map((f) => [f.key, values[f.key]]),
            ),
          );
          onClose();
        } catch (e: any) {
          setError(e.message || "请检查表单");
        } finally {
          setBusy(false);
        }
      }}
    >
      {warning && (
        <Alert type="warning" content={warning} style={{ marginBottom: 20 }} />
      )}
      {error && (
        <Alert type="error" content={error} style={{ marginBottom: 16 }} />
      )}
      <Form
        form={form}
        layout="vertical"
        initialValues={initial}
        onValuesChange={(_, all) => setValues(all)}
      >
        {visibleFields.map((f) => (
          <Form.Item
            key={f.key}
            field={f.key}
            label={f.label}
            extra={typeof f.help === "function" ? f.help(values) : f.help}
            rules={
              f.required
                ? [{ required: true, message: `请填写${f.label}` }]
                : []
            }
            triggerPropName={f.type === "switch" ? "checked" : "value"}
          >
            {f.type === "select" ? (
              <Select
                aria-label={f.label}
                options={f.options}
                disabled={f.disabled}
              />
            ) : f.type === "number" ? (
              <InputNumber min={f.min ?? 0} max={f.max ?? 10000} />
            ) : f.type === "switch" ? (
              <Switch aria-label={f.label} />
            ) : f.type === "password" ? (
              <Input.Password autoComplete="new-password" />
            ) : f.type === "textarea" ? (
              <Input.TextArea autoSize={{ minRows: 3, maxRows: 8 }} />
            ) : (
              <Input />
            )}
          </Form.Item>
        ))}
      </Form>
    </Modal>
  );
}
export function useData<T = any>(path: string, revision: number) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    api<T>(path)
      .then((d) => {
        if (active) {
          setData(d);
          setError("");
        }
      })
      .catch((e) => active && setError(e.message));
    return () => {
      active = false;
    };
  }, [path, revision]);
  return { data, error };
}
export function ErrorBox({ error }: { error?: string }) {
  return error ? (
    <Alert
      type="error"
      title="无法读取数据"
      content={error}
      style={{ marginBottom: 20 }}
    />
  ) : null;
}
export function Status({ value }: { value: string }) {
  const labels: Row = {
    succeeded: "已完成",
    queued: "排队中",
    running: "执行中",
    failed: "失败",
    interrupted: "已中断",
    ONLINE: "健康",
    DEGRADED: "降级",
    FAULTED: "故障",
  };
  return (
    <Tag
      color={
        ["succeeded", "ONLINE", "active"].includes(value)
          ? "green"
          : ["failed", "interrupted", "FAULTED"].includes(value)
            ? "red"
            : value === "running"
              ? "arcoblue"
              : "gray"
      }
    >
      {labels[value] || value || "—"}
    </Tag>
  );
}
export type SearchField = {
  key: string;
  label: string;
  value?: (row: Row) => unknown;
};
type SearchCondition = { field: string; value: string };
export function DataTable({
  rows,
  columns,
  loading = false,
  rowKey = "id",
  tableName = "记录",
  searchText,
  endpoint,
  revision = 0,
  filterKey,
  searchFields,
}: {
  rows: any[] | null;
  columns: any[];
  loading?: boolean;
  rowKey?: string;
  tableName?: string;
  searchText?: (row: Row) => string;
  endpoint?: string;
  revision?: number;
  filterKey?: string;
  searchFields?: SearchField[];
}) {
  const [keyword, setKeyword] = useState("");
  const [query, setQuery] = useState("");
  const [conditions, setConditions] = useState<SearchCondition[]>([]);
  const [activeField, setActiveField] = useState("");
  const [choosingField, setChoosingField] = useState(false);
  const inputRef = useRef<any>(null);
  const fields: SearchField[] =
    searchFields ||
    columns
      .filter((c) => c.dataIndex && typeof c.title === "string")
      .map((c) => ({ key: c.dataIndex, label: c.title }));
  const fieldLabel = (key: string) =>
    fields.find((f) => f.key === key)?.label || "关键词";
  const addCondition = () => {
    const value = keyword.trim();
    if (!value || conditions.length >= 20) return;
    setConditions((items) =>
      items.some((c) => c.field === activeField && c.value === value)
        ? items
        : [...items, { field: activeField, value }],
    );
    setKeyword("");
    setQuery("");
    setActiveField("");
    setPage(1);
    setChoosingField(false);
  };
  const effectiveConditions = [
    ...conditions,
    ...(query ? [{ field: activeField, value: query }] : []),
  ];
  const encodedConditions = JSON.stringify(
    effectiveConditions.map((condition) => {
      const labels: Record<string, string> =
        condition.field === "kind"
          ? kindLabels
          : condition.field === "action"
            ? actionLabels
            : condition.field === "status" || condition.field === "phase"
              ? {
                  queued: "排队中",
                  running: "执行中",
                  succeeded: "已完成",
                  failed: "失败",
                  interrupted: "已中断",
                }
              : condition.field === "severity"
                ? { critical: "严重", warning: "警告" }
                : {};
      const match = Object.entries(labels).find(
        ([, label]) => label === condition.value,
      );
      return { ...condition, value: match?.[0] || condition.value };
    }),
  );
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(10);
  useEffect(() => setPage(1), [filterKey]);
  const [remote, setRemote] = useState<{ items: Row[]; total: number } | null>(
    null,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQuery(keyword.trim()), 250);
    return () => clearTimeout(timer);
  }, [keyword]);
  useEffect(() => {
    if (!endpoint) return;
    let active = true;
    setBusy(true);
    const q = new URLSearchParams({
      page: String(page),
      page_size: String(size),
      filters: encodedConditions,
    });
    api(endpoint + "?" + q)
      .then((data) => {
        if (!active) return;
        const result = Array.isArray(data)
          ? { items: data, total: data.length }
          : data;
        setRemote(result);
        setError("");
        const last = Math.max(1, Math.ceil(result.total / size));
        if (page > last) setPage(last);
      })
      .catch((e) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setBusy(false);
      });
    return () => {
      active = false;
    };
  }, [endpoint, revision, page, size, encodedConditions]);
  const filtered = useMemo(() => {
    if (endpoint) return remote?.items || [];
    const checks = [
      ...conditions,
      ...(keyword.trim()
        ? [{ field: activeField, value: keyword.trim() }]
        : []),
    ];
    return (rows || []).filter((r) =>
      checks.every((condition) => {
        const field = fields.find((f) => f.key === condition.field);
        const raw = field
          ? field.value
            ? field.value(r)
            : field.key.split(".").reduce((v: any, k) => v?.[k], r)
          : JSON.stringify(r) + " " + (searchText?.(r) || "");
        return String(raw ?? "")
          .toLocaleLowerCase()
          .includes(condition.value.toLocaleLowerCase());
      }),
    );
  }, [
    rows,
    keyword,
    activeField,
    conditions,
    searchText,
    fields,
    endpoint,
    remote,
  ]);
  const hasSearch = conditions.length > 0 || !!keyword;
  const total = endpoint ? remote?.total || 0 : filtered.length;
  const pages = Math.max(1, Math.ceil(total / size));
  const current = Math.min(page, pages);
  useEffect(() => {
    if (!endpoint && page > pages) setPage(pages);
  }, [endpoint, page, pages]);
  return (
    <div className="data-table" aria-label={tableName + "表格"}>
      <div className="table-tools">
        <Popover
          trigger="click"
          position="bl"
          popupVisible={choosingField}
          onVisibleChange={setChoosingField}
          content={
            <div className="search-field-menu" aria-label="搜索项">
              <span className="muted">
                选择搜索项，输入值后按回车添加；多个条件同时满足
              </span>
              {[{ key: "", label: "关键词" }, ...fields].map((field) => (
                <Button
                  key={field.key}
                  type="text"
                  onClick={() => {
                    setActiveField(field.key);
                    setChoosingField(false);
                    inputRef.current?.focus();
                  }}
                >
                  {field.label}
                </Button>
              ))}
            </div>
          }
        >
          <div
            className="table-search"
            onClick={() => inputRef.current?.focus()}
          >
            {conditions.map((condition, index) => (
              <Tag
                key={index}
                closable
                onClose={(e) => {
                  e.stopPropagation();
                  setConditions((items) => items.filter((_, i) => i !== index));
                  setPage(1);
                }}
              >
                {fieldLabel(condition.field)}：{condition.value}
              </Tag>
            ))}
            {activeField && (
              <Tag color="arcoblue">{fieldLabel(activeField)}</Tag>
            )}
            <Input
              ref={inputRef}
              aria-label={"筛选" + tableName}
              placeholder={
                activeField
                  ? `输入${fieldLabel(activeField)}，回车添加`
                  : "点击选择搜索项，输入值后回车添加"
              }
              disabled={conditions.length >= 20}
              maxLength={200}
              value={keyword}
              onChange={(v) => {
                setKeyword(v);
                setPage(1);
              }}
              onPressEnter={addCondition}
            />
            {hasSearch && (
              <Button
                type="text"
                aria-label={"清空" + tableName + "搜索"}
                onClick={(e) => {
                  e.stopPropagation();
                  setConditions([]);
                  setKeyword("");
                  setActiveField("");
                  setPage(1);
                  setChoosingField(false);
                }}
              >
                清空
              </Button>
            )}
            <Button
              type="text"
              aria-label={"添加" + tableName + "搜索条件"}
              disabled={!keyword.trim() || conditions.length >= 20}
              onClick={(e) => {
                e.stopPropagation();
                addCondition();
              }}
            >
              添加条件
            </Button>
          </div>
        </Popover>
      </div>
      <ErrorBox error={error} />
      <Table
        rowKey={rowKey}
        data={
          endpoint
            ? filtered
            : filtered.slice((current - 1) * size, current * size)
        }
        columns={columns}
        loading={loading || (endpoint ? busy : rows === null)}
        pagination={false}
        noDataElement={
          <div className="empty">
            {hasSearch ? "没有匹配的记录" : "暂无记录"}
          </div>
        }
        scroll={{ x: 900 }}
      />
      <div className="table-footer">
        <span role="status">
          共 {total} 条
          {!endpoint && hasSearch && rows ? `（筛选前 ${rows.length} 条）` : ""}{" "}
          · 第 {current} / {pages} 页
        </span>
        <Pagination
          current={current}
          total={total}
          pageSize={size}
          hideOnSinglePage={false}
          showJumper
          sizeCanChange
          sizeOptions={[10, 20, 50, 100]}
          onChange={(p, s) => {
            setPage(s !== size ? 1 : p);
            setSize(s);
          }}
          onPageSizeChange={(s) => {
            setSize(s);
            setPage(1);
          }}
        />
      </div>
    </div>
  );
}
export function Chart({ data }: { data: any[] | null }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current);
    chart.setOption({
      animation: false,
      color: ["#17857a"],
      tooltip: { trigger: "axis", valueFormatter: (v: number) => bytes(v) },
      grid: { left: 68, right: 22, top: 20, bottom: 30 },
      xAxis: {
        type: "category",
        data: (data || []).map((m) =>
          new Date(m.time).toLocaleTimeString("zh-CN", {
            hour: "2-digit",
            minute: "2-digit",
          }),
        ),
        axisLine: { lineStyle: { color: "#dce3eb" } },
        axisLabel: { color: "#8490a2" },
      },
      yAxis: {
        type: "value",
        axisLabel: { formatter: (n: number) => bytes(n), color: "#8490a2" },
        splitLine: { lineStyle: { color: "#eef2f6" } },
      },
      series: [
        {
          name: "已用容量",
          type: "line",
          showSymbol: false,
          smooth: true,
          data: (data || []).map((m) => m.used),
          areaStyle: { opacity: 0.09 },
          lineStyle: { width: 2 },
        },
      ],
    });
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(ref.current);
    return () => {
      observer.disconnect();
      chart.dispose();
    };
  }, [data]);
  return <div className="chart" ref={ref} />;
}
export function JobNotice({ job }: { job: any }) {
  return <span>任务 #{job.id} 已加入队列，可在任务中心查看结果。</span>;
}
export const jobColumns = [
  { title: "任务", dataIndex: "kind", render: actionLabel },
  { title: "目标", dataIndex: "target" },
  {
    title: "状态",
    dataIndex: "status",
    render: (v: string) => <Status value={v} />,
  },
  { title: "提交时间", dataIndex: "created_at", render: date },
  {
    title: "执行结果",
    render: (_: any, r: Row) => (
      <span className={r.error ? "text-danger" : ""}>
        {r.error || r.result || "—"}
      </span>
    ),
  },
];
export function Browser({
  snapshot,
  onClose,
}: {
  snapshot: Row;
  onClose: () => void;
}) {
  const [path, setPath] = useState(".");
  const q = `repository_id=${snapshot.repository_id}&snapshot=${encodeURIComponent(snapshot.name)}&path=${encodeURIComponent(path)}`;
  const { data, error } = useData("/snapshots/browse?" + q, 0);
  return (
    <Modal
      visible
      footer={null}
      title="浏览快照文件"
      onCancel={onClose}
      style={{ width: 900 }}
    >
      <div className="browse-path">
        <Button
          disabled={path === "."}
          onClick={() => setPath(path.split("/").slice(0, -1).join("/") || ".")}
        >
          上一级
        </Button>
        <code>{path}</code>
      </div>
      <ErrorBox error={error} />
      <DataTable
        rows={data?.entries || []}
        rowKey="name"
        columns={[
          {
            title: "名称",
            dataIndex: "name",
            render: (v: string, r: Row) =>
              r.directory ? (
                <Button
                  type="text"
                  onClick={() => setPath((path === "." ? "" : path + "/") + v)}
                >
                  ▸ {v}
                </Button>
              ) : (
                <span>
                  {r.symlink ? "↗ " : ""}
                  {v}
                </span>
              ),
          },
          { title: "大小", dataIndex: "size", render: bytes },
          { title: "修改时间", dataIndex: "modified_at", render: date },
          {
            title: "恢复",
            render: (_: any, r: Row) =>
              !r.directory && !r.symlink ? (
                <a
                  href={
                    "/api/snapshots/download?" +
                    `repository_id=${snapshot.repository_id}&snapshot=${encodeURIComponent(snapshot.name)}&path=${encodeURIComponent((path === "." ? "" : path + "/") + r.name)}`
                  }
                >
                  下载文件
                </a>
              ) : null,
          },
        ]}
      />
      {data?.truncated && (
        <Alert content="目录超过 2000 项，请进入子目录浏览。" />
      )}
    </Modal>
  );
}
