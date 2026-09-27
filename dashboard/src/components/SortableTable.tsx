import { useMemo, useState, type ReactNode } from "react";

export interface Column<T> {
  key: string;
  header: string;
  /** Sort value; null sorts last. Omit to make the column unsortable. */
  sortValue?: (row: T) => string | number | null;
  render: (row: T) => ReactNode;
  align?: "left" | "right";
}

interface Props<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string | number;
  initialSort?: { key: string; dir: "asc" | "desc" };
  empty?: ReactNode;
  caption: string;
}

type Sort = { key: string; dir: "asc" | "desc" } | null;

function compare(a: string | number | null, b: string | number | null): number {
  if (a === b) return 0;
  if (a === null) return 1; // nulls last in both directions
  if (b === null) return -1;
  if (typeof a === "number" && typeof b === "number") return a - b;
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: "base" });
}

/** A table whose sortable headers toggle ascending/descending on click. */
export function SortableTable<T>({ columns, rows, rowKey, initialSort, empty, caption }: Props<T>) {
  const [sort, setSort] = useState<Sort>(initialSort ?? null);

  const sorted = useMemo(() => {
    const col = sort && columns.find((c) => c.key === sort.key);
    if (!sort || !col?.sortValue) return rows;
    const value = col.sortValue;
    const out = [...rows].sort((a, b) => {
      const va = value(a);
      const vb = value(b);
      if (va === null || vb === null) return compare(va, vb); // nulls last regardless of direction
      return sort.dir === "asc" ? compare(va, vb) : compare(vb, va);
    });
    return out;
  }, [rows, sort, columns]);

  const toggle = (key: string) =>
    setSort((s) => (s?.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: "desc" }));

  if (rows.length === 0) return <div className="empty">{empty ?? "No data."}</div>;

  return (
    <div className="table-wrap">
      <table>
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            {columns.map((c) => {
              const active = sort?.key === c.key;
              const ariaSort = active ? (sort.dir === "asc" ? "ascending" : "descending") : "none";
              return (
                <th key={c.key} className={c.align === "right" ? "num" : undefined} aria-sort={c.sortValue ? ariaSort : undefined} scope="col">
                  {c.sortValue ? (
                    <button type="button" className="sort" onClick={() => toggle(c.key)}>
                      {c.header}
                      <span aria-hidden="true" className={active ? "arrow on" : "arrow"}>
                        {active && sort.dir === "asc" ? "▲" : "▼"}
                      </span>
                    </button>
                  ) : (
                    c.header
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {sorted.map((row) => (
            <tr key={rowKey(row)}>
              {columns.map((c) => (
                <td key={c.key} className={c.align === "right" ? "num" : undefined} data-label={c.header}>
                  {c.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
