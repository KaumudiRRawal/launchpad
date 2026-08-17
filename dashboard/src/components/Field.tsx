import { useId } from 'react'
import type { ReactElement, ReactNode } from 'react'

interface FieldProps {
  label: string
  hint?: string
  error?: string | undefined
  /** Receives the id to attach, so the label and the control stay associated. */
  children: (id: string) => ReactElement
}

/**
 * Field pairs a label with a control and the API's complaint about it. The
 * child is a function of the generated id rather than arbitrary markup, which
 * makes it impossible to render a labelled field whose label points at nothing.
 */
export function Field({ label, hint, error, children }: FieldProps) {
  const id = useId()

  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id)}
      {renderNote(hint, error)}
    </div>
  )
}

function renderNote(hint: string | undefined, error: string | undefined): ReactNode {
  if (error !== undefined) {
    return <p className="field-error">{error}</p>
  }
  if (hint !== undefined) {
    return <p className="field-hint">{hint}</p>
  }
  return null
}
