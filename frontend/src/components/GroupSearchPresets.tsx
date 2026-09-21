import { DeleteOutlined, DownOutlined } from '@ant-design/icons'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Alert, App, Button, Checkbox, Empty, Input, Modal, Popconfirm, Popover, Space, Spin, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { api } from '../api/client'
import type { GroupQueryFilter, GroupSearchPreset } from '../api/types'
import { displayError } from '../utils/format'
import { summarizeGroupQuery, type GroupQuerySummaryLabels } from '../utils/groupQuery'
import { combineSearchPresets } from '../utils/groupSearchPresets'

interface Props {
  selectedIds: string[]
  saveFilter?: GroupQueryFilter
  labels: GroupQuerySummaryLabels
  onApply: (filter: GroupQueryFilter | undefined, ids: string[]) => void
  onSaveClose: () => void
  onDetach: () => void
}

const queryKey = ['group-search-presets']

export function GroupSearchPresets({ selectedIds, saveFilter, labels, onApply, onSaveClose, onDetach }: Props) {
  const { message } = App.useApp()
  const client = useQueryClient()
  const [open, setOpen] = useState(false)
  const [checkedIds, setCheckedIds] = useState<string[]>([])
  const [name, setName] = useState('')
  const [error, setError] = useState<string>()
  const presets = useQuery({ queryKey, queryFn: api.getSearchPresets, enabled: open, staleTime: 0 })
  useEffect(() => { if (saveFilter) setName('') }, [saveFilter])

  const save = useMutation({
    mutationFn: api.createSearchPreset,
    onSuccess: (preset) => {
      client.setQueryData<GroupSearchPreset[]>(queryKey, (items = []) => [...items, preset].sort((a, b) => a.name.localeCompare(b.name)))
      void client.invalidateQueries({ queryKey })
      void message.success('预设已保存，可从“搜索预设”勾选应用')
      onSaveClose()
    },
  })
  const remove = useMutation({
    mutationFn: api.deleteSearchPreset,
    onSuccess: (_, id) => {
      client.setQueryData<GroupSearchPreset[]>(queryKey, (items = []) => items.filter((item) => item.id !== id))
      setCheckedIds((ids) => ids.filter((item) => item !== id))
      if (selectedIds.includes(id)) onDetach()
      void message.success(selectedIds.includes(id) ? '预设已删除，当前搜索条件保留' : '预设已删除')
    },
    onError: (err) => void message.error(displayError(err)),
  })
  const apply = () => {
    try {
      const filters = checkedIds.map((id) => {
        const preset = presets.data?.find((item) => item.id === id)
        if (!preset) throw new Error('部分预设已被删除，请重新勾选')
        return preset.filter
      })
      onApply(combineSearchPresets(filters), checkedIds)
      setOpen(false)
    } catch (err) {
      setError(displayError(err))
    }
  }
  const submit = () => {
    if (saveFilter && name.trim() && !save.isPending) save.mutate({ name: name.trim(), filter: saveFilter })
  }

  return (
    <>
      <Popover trigger="click" placement="bottomLeft" open={open}
        onOpenChange={(value) => { setOpen(value); if (value) { setCheckedIds(selectedIds); setError(undefined) } }}
        content={(
          <div className="search-presets-panel" role="group" aria-label="已保存的搜索预设">
            <Typography.Text strong>勾选搜索预设</Typography.Text>
            <Typography.Paragraph type="secondary">多选时同时满足所有预设。应用后替换当前高级条件，保留关键词、运行状态和下载器筛选。</Typography.Paragraph>
            {presets.isLoading ? <Spin /> : presets.isError ? (
              <Alert type="error" message="预设加载失败" description={displayError(presets.error)}
                action={<Button onClick={() => void presets.refetch()}>重试</Button>} />
            ) : !presets.data?.length ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无预设，请在高级搜索中配置条件并保存。" />
            ) : (
              <div className="search-presets-list">
                {presets.data.map((preset) => (
                  <div className="search-preset-row" key={preset.id}>
                    <Checkbox checked={checkedIds.includes(preset.id)} disabled={remove.isPending}
                      onChange={(event) => {
                        setCheckedIds((ids) => event.target.checked ? [...ids, preset.id] : ids.filter((id) => id !== preset.id))
                        setError(undefined)
                      }}>
                      <strong>{preset.name}</strong>
                      <small>{summarizeGroupQuery(preset.filter, labels)}</small>
                    </Checkbox>
                    <Popconfirm title={`删除预设“${preset.name}”？`} description="已应用的搜索条件会保留。" okText="删除" cancelText="取消"
                      onConfirm={() => remove.mutate(preset.id)}>
                      <Button type="text" danger size="small" icon={<DeleteOutlined />} aria-label={`删除预设 ${preset.name}`}
                        disabled={remove.isPending} />
                    </Popconfirm>
                  </div>
                ))}
              </div>
            )}
            {error && <Alert type="error" showIcon message={error} />}
            <div className="search-presets-actions">
              <Button disabled={remove.isPending} onClick={() => { setCheckedIds([]); setError(undefined) }}>取消勾选</Button>
              <Button type="primary" disabled={presets.isFetching || presets.isError || remove.isPending} onClick={apply}>应用预设</Button>
            </div>
          </div>
        )}>
        <Button aria-label="搜索预设" type={selectedIds.length ? 'primary' : 'default'}>搜索预设{selectedIds.length ? ` (${selectedIds.length})` : ''} <DownOutlined /></Button>
      </Popover>
      <Modal title="保存搜索预设" open={Boolean(saveFilter)} zIndex={1200} okText="保存预设" cancelText="取消"
        confirmLoading={save.isPending} okButtonProps={{ disabled: !name.trim() }}
        closable={!save.isPending} maskClosable={!save.isPending} keyboard={!save.isPending}
        cancelButtonProps={{ disabled: save.isPending }}
        onCancel={() => { if (!save.isPending) { save.reset(); onSaveClose() } }} onOk={submit}
        afterClose={() => save.reset()}>
        <Space direction="vertical" size={12} className="modal-stack">
          <Typography.Text>保存到服务端，可在其他浏览器使用。仅保存高级条件。</Typography.Text>
          <Input aria-label="预设名称" placeholder="预设名称，例如：低辅种电影" maxLength={80} value={name}
            disabled={save.isPending} onChange={(event) => setName(event.target.value)} onPressEnter={submit} />
          <Typography.Paragraph type="secondary">{summarizeGroupQuery(saveFilter, labels)}</Typography.Paragraph>
          {save.error && <Alert type="error" showIcon message={displayError(save.error)} />}
        </Space>
      </Modal>
    </>
  )
}
