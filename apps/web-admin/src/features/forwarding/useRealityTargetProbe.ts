import { onUnmounted, ref, watch, type ComputedRef } from 'vue'
import { businessApi, type ForwardRuleInput, type RealityProbe, type RealityTarget } from '@/api/business'
import { displayError } from '@/lib/displayFormatters'

// These checks describe control-plane TLS reachability, never node readiness.
export function useRealityTargetProbe(form: ForwardRuleInput, enabled: ComputedRef<boolean>, canRandomize: boolean) {
  const realityTargets = ref<RealityTarget[]>([])
  const candidateProbes = ref<RealityProbe[]>([])
  const currentProbe = ref<RealityProbe | null>(null)
  const probeError = ref('')
  const loadingRealityTargets = ref(false)
  const testingReality = ref(false)
  let timer: ReturnType<typeof setTimeout> | undefined
  let currentRequest: AbortController | undefined
  let candidatesRequest: AbortController | undefined
  let version = 0
  let poolVersion = 0
  let closed = false
  const signature = () => `${form.reality_server_name?.trim() ?? ''}\n${form.reality_destination?.trim() ?? ''}`
  const selected = (): RealityTarget => ({ server_name: form.reality_server_name?.trim() ?? '', destination: form.reality_destination?.trim() ?? '' })

  function invalidateCurrent() {
    version++
    clearTimeout(timer)
    currentRequest?.abort()
    testingReality.value = false
  }

  async function testCurrentReality(): Promise<void> {
    invalidateCurrent()
    currentProbe.value = null
    probeError.value = ''
    if (!enabled.value || !form.reality_server_name?.trim()) return
    const id = version
    const controller = new AbortController()
    currentRequest = controller
    testingReality.value = true
    try {
      const result = await businessApi.probeReality(selected(), controller.signal)
      if (!closed && id === version) currentProbe.value = result
    } catch (cause) {
      if (!closed && id === version) probeError.value = displayError(cause)
    } finally {
      if (!closed && id === version) testingReality.value = false
    }
  }

  watch(() => [enabled.value, form.reality_server_name, form.reality_destination], () => {
    invalidateCurrent()
    currentProbe.value = null
    probeError.value = ''
    if (enabled.value && form.reality_server_name?.trim()) timer = setTimeout(() => { void testCurrentReality() }, 500)
  }, { immediate: true, flush: 'sync' })

  async function randomizeReality(): Promise<void> {
    if (!canRandomize || !enabled.value || loadingRealityTargets.value) return
    loadingRealityTargets.value = true
    candidateProbes.value = []
    probeError.value = ''
    const before = signature()
    const id = ++poolVersion
    const controller = new AbortController()
    candidatesRequest = controller
    try {
      if (!realityTargets.value.length) realityTargets.value = (await businessApi.realityTargets()).items
      if (closed || id !== poolVersion || !enabled.value || controller.signal.aborted) return
      const results = await Promise.all(realityTargets.value.map(async (target): Promise<RealityProbe> => {
        try { return await businessApi.probeReality(target, controller.signal) }
        catch (cause) { return { ...target, compatible: false, latency_ms: 0, source: 'control_plane', checked_at: '', message: displayError(cause) } }
      }))
      if (closed || id !== poolVersion || !enabled.value || controller.signal.aborted) return
      candidateProbes.value = results
      // A slow candidate test must never overwrite a manual edit made meanwhile.
      if (signature() !== before) return
      const passed = results.filter((item) => item.compatible)
      const different = passed.filter((item) => item.server_name !== form.reality_server_name)
      const pool = different.length ? different : passed
      if (!pool.length) {
        probeError.value = '候选目标均未检测通过，请手动填写后复测；也可稍后重新检测。'
        return
      }
      const target = pool[Math.floor(Math.random() * pool.length)]!
      form.reality_server_name = target.server_name
      form.reality_destination = target.destination
      invalidateCurrent()
      currentProbe.value = target
    } catch (cause) {
      if (!closed && id === poolVersion) probeError.value = displayError(cause)
    } finally {
      if (!closed && id === poolVersion) loadingRealityTargets.value = false
    }
  }

  async function loadCreationRealityTarget(): Promise<void> {
    if (!form.reality_server_name && !form.reality_destination) await randomizeReality()
  }

  watch(enabled, (value) => {
    if (!value) {
      poolVersion++
      candidatesRequest?.abort()
      loadingRealityTargets.value = false
      candidateProbes.value = []
    }
  }, { flush: 'sync' })
  onUnmounted(() => {
    closed = true
    invalidateCurrent()
    candidatesRequest?.abort()
  })
  return { realityTargets, candidateProbes, currentProbe, probeError, loadingRealityTargets, testingReality, randomizeReality, loadCreationRealityTarget, testCurrentReality }
}
