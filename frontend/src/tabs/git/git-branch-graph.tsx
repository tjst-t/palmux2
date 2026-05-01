// SVG branch graph (S013-1-11).
//
// Computes a "lane index" for each commit based on a tiny topological
// sort: the first time we see a hash, we assign it the next free lane;
// when a parent is referenced, the child claims the same lane (so a
// linear history collapses to lane 0). Branches that diverge get
// adjacent lanes.
//
// The result is rendered as an absolutely-positioned SVG overlay. The
// list rows in git-log align row-by-row with the graph because we draw
// each commit at row index * ROW_H. We deliberately don't try to do
// anything fancy with merge layouts — palmux2's graph is "simple
// timeline + occasional fork", not Sourcetree-grade.

import { useMemo } from 'react'

import type { LogEntryDetail } from './types'

interface Props {
  entries: LogEntryDetail[]
  className?: string
}

// Per-row height (must match the height of one .row in the log CSS,
// approximately 22px including padding). Slight overshoot is fine — the
// graph just needs to draw lines that "feel" continuous.
const ROW_H = 24
const LANE_W = 14
// Leave a little space at the top so the dot is centred on the row.
const TOP_PAD = 12

interface Placed {
  hash: string
  parents: string[]
  lane: number
  row: number
}

export function GitBranchGraph({ entries, className }: Props) {
  const placed = useMemo(() => placeEntries(entries), [entries])
  if (placed.length === 0) return null
  const maxLane = placed.reduce((m, p) => Math.max(m, p.lane), 0)
  const width = (maxLane + 1) * LANE_W + LANE_W
  const height = placed.length * ROW_H + TOP_PAD

  // Build edges: child commit row → parent commit row, by hash lookup.
  const byHash = new Map(placed.map((p) => [p.hash, p]))
  const edges: { x1: number; y1: number; x2: number; y2: number; lane: number }[] = []
  for (const p of placed) {
    for (const par of p.parents) {
      const pp = byHash.get(par)
      if (!pp) continue
      edges.push({
        x1: laneX(p.lane),
        y1: rowY(p.row),
        x2: laneX(pp.lane),
        y2: rowY(pp.row),
        lane: p.lane,
      })
    }
  }

  return (
    <svg
      className={className}
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      data-testid="git-branch-graph"
      style={{ flexShrink: 0 }}
    >
      {edges.map((e, i) => (
        <path
          key={i}
          d={edgePath(e)}
          stroke={laneColor(e.lane)}
          strokeWidth={1.5}
          fill="none"
        />
      ))}
      {placed.map((p) => (
        <circle
          key={p.hash}
          cx={laneX(p.lane)}
          cy={rowY(p.row)}
          r={4}
          fill={laneColor(p.lane)}
          stroke="var(--color-bg, #0f1117)"
          strokeWidth={1.5}
          data-row={p.row}
        />
      ))}
    </svg>
  )
}

function placeEntries(entries: LogEntryDetail[]): Placed[] {
  const placed: Placed[] = []
  // Track which lane each pending parent occupies so the next entry that
  // claims that parent reuses the lane.
  const lanesByExpected = new Map<string, number>()
  let nextFreeLane = 0
  const usedLanes = new Set<number>()

  for (let i = 0; i < entries.length; i++) {
    const e = entries[i]
    let lane: number
    const expected = lanesByExpected.get(e.hash)
    if (expected !== undefined) {
      lane = expected
      lanesByExpected.delete(e.hash)
    } else {
      // Take the lowest free lane.
      lane = 0
      while (usedLanes.has(lane)) lane++
      if (lane >= nextFreeLane) nextFreeLane = lane + 1
    }
    usedLanes.add(lane)
    placed.push({ hash: e.hash, parents: e.parents, lane, row: i })

    // For each parent: the first parent claims this lane; later parents
    // (merges) get fresh lanes.
    let firstParent = true
    for (const par of e.parents) {
      if (lanesByExpected.has(par)) continue
      if (firstParent) {
        lanesByExpected.set(par, lane)
        firstParent = false
      } else {
        let l = 0
        while (usedLanes.has(l) || [...lanesByExpected.values()].includes(l)) l++
        lanesByExpected.set(par, l)
      }
    }
    // If no parent claims this lane (i.e. the commit is a tip we're done
    // with), free it.
    if (![...lanesByExpected.values()].includes(lane)) {
      usedLanes.delete(lane)
    }
  }
  return placed
}

function laneX(lane: number): number {
  return LANE_W / 2 + lane * LANE_W
}
function rowY(row: number): number {
  return TOP_PAD + row * ROW_H
}

function edgePath(e: { x1: number; y1: number; x2: number; y2: number }): string {
  if (e.x1 === e.x2) {
    return `M ${e.x1} ${e.y1} L ${e.x2} ${e.y2}`
  }
  // Knee: vertical down most of the way, then a curve to the parent's lane.
  const midY = (e.y1 + e.y2) / 2
  return `M ${e.x1} ${e.y1} C ${e.x1} ${midY}, ${e.x2} ${midY}, ${e.x2} ${e.y2}`
}

const LANE_COLORS = [
  '#7c8aff',
  '#64d2a0',
  '#e8b45a',
  '#ef4444',
  '#9ba6ff',
  '#a78bfa',
]

function laneColor(lane: number): string {
  return LANE_COLORS[lane % LANE_COLORS.length]
}
