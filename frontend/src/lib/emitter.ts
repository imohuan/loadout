import mitt from 'mitt'

type Events = {
  unauthorized: undefined
  refresh: string
}

export const emitter = mitt<Events>()
