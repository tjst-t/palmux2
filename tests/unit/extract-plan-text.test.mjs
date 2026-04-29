// Standalone test of the extractPlanText logic against the actual ExitPlanMode
// payload schema reported by the user.
//
// The CLI's ExitPlanMode input has the shape:
//   {"allowedPrompts":[...],"plan":"# Plan...","planFilePath":"..."}
// during streaming the partial may only contain allowedPrompts before plan
// arrives — the bug was that extractPlanText fell back to raw text and
// leaked the JSON into the UI.

// Mirror the implementation from blocks.tsx exactly.
function parseInputObject(block) {
  const raw = block.input
  if (raw == null) return null
  if (typeof raw === 'object') return raw
  if (typeof raw === 'string') {
    try { return JSON.parse(raw) } catch { return null }
  }
  return null
}

function extractPlanText(block) {
  const obj = parseInputObject(block)
  if (obj) {
    if (typeof obj.plan === 'string') return obj.plan
    if (typeof obj.markdown === 'string') return obj.markdown
    if (typeof obj.content === 'string') return obj.content
  }
  const text = block.text ?? ''
  if (!text) return ''
  const trimmed = text.trim()
  if (trimmed.startsWith('{')) {
    try {
      const parsed = JSON.parse(trimmed)
      if (typeof parsed.plan === 'string') return parsed.plan
      if (typeof parsed.markdown === 'string') return parsed.markdown
      if (typeof parsed.content === 'string') return parsed.content
    } catch {
      const m = trimmed.match(/"plan"\s*:\s*"((?:[^"\\]|\\.)*)/)
      if (m) {
        try { return JSON.parse(`"${m[1]}"`) } catch { return m[1] }
      }
    }
    return ''
  }
  return text
}

const cases = [
  {
    name: 'finalised block with plan field',
    block: {
      input: {
        allowedPrompts: [{tool:"Bash",prompt:"restart dev servers via make serve"}],
        plan: "# Plan: Add logout button\n\n- Step 1\n- Step 2",
        planFilePath: "/tmp/plan.md",
      },
      text: '',
    },
    expect: "# Plan: Add logout button\n\n- Step 1\n- Step 2",
    why: 'finalised: plan field present in input',
  },
  {
    name: 'streaming partial with only allowedPrompts',
    block: {
      input: undefined,
      text: '{"allowedPrompts":[{"tool":"Bash","prompt":"restart dev servers via make serve"}]}',
    },
    expect: '',
    why: 'partial JSON without plan key — must NOT leak raw JSON',
  },
  {
    name: 'streaming partial mid-plan',
    block: {
      input: undefined,
      text: '{"allowedPrompts":[{"tool":"Bash"}],"plan":"# Plan: Add logout',
    },
    expect: '# Plan: Add logout',
    why: 'partial JSON with truncated plan string — extract what we have',
  },
  {
    name: 'streaming partial JSON not yet a value',
    block: {
      input: undefined,
      text: '{',
    },
    expect: '',
    why: 'incomplete JSON — show nothing',
  },
  {
    name: 'finalised block with markdown field (variant)',
    block: {
      input: {markdown: '# Test plan'},
      text: '',
    },
    expect: '# Test plan',
    why: 'fallback markdown key still works',
  },
  {
    name: 'plain text plan (legacy)',
    block: {
      input: undefined,
      text: '# Hello plain plan',
    },
    expect: '# Hello plain plan',
    why: 'non-JSON text passes through',
  },
]

let pass = 0
let fail = 0
for (const c of cases) {
  const got = extractPlanText(c.block)
  if (got === c.expect) {
    console.log(`✅ ${c.name}`)
    console.log(`   ${c.why}`)
    pass++
  } else {
    console.log(`❌ ${c.name}`)
    console.log(`   ${c.why}`)
    console.log(`   expected: ${JSON.stringify(c.expect)}`)
    console.log(`   got:      ${JSON.stringify(got)}`)
    fail++
  }
}

console.log(`\n${pass}/${pass+fail} cases passed`)
process.exit(fail === 0 ? 0 : 1)
