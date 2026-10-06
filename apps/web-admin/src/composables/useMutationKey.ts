/** A failed request can be retried safely. Editing its payload starts a new operation. */
export function useMutationKey(): (payload: unknown) => string {
  let previous = ''
  let key = ''
  return (payload) => {
    const serialized = JSON.stringify(payload)
    if (!key || previous !== serialized) {
      previous = serialized
      key = crypto.randomUUID()
    }
    return key
  }
}
