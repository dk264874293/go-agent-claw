"""来源审查协议2：只读校验、完整双遍封存、程序派生判断；不调用模型。"""
import hashlib
import json
import os
import re
from pathlib import Path

SCORES=('coverage','citation','correctness','conditions','important_findings')
SPECIAL=('proposed_repairs','subject_identity','important_omissions')


def canonical(value):
    return json.dumps(value,ensure_ascii=False,sort_keys=True,separators=(',',':'))


def digest(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def semantic_projection(review):
    """措辞、时间和展示排序不是语义；来源位置、判断和归属属于语义。"""
    findings=[]
    for item in review['findings']:
        row={k:item[k] for k in ('id','kind','verdict','severity','rubric_id','claim','start_byte','end_byte') if k in item}
        for key in ('issue_types','rubric_ids'):
            if key in item:row[key]=sorted(item[key])
        row['evidence']=sorted(item['evidence'],key=canonical)
        findings.append(row)
    return {'review_id':review['review_id'],'artifact_sha256':review['artifact_sha256'],
            'reviewer':review['reviewer'],'method':review['method'],'scores':review['scores'],
            'score_basis':{k:sorted(v['finding_ids']) for k,v in review['score_basis'].items()},
            'findings':sorted(findings,key=lambda v:v['id']),
            'rubric_assessments':{k:{'status':v['status'],'severity':v['severity'],'finding_ids':sorted(v['finding_ids'])} for k,v in review['rubric_assessments'].items()},
            'quality_checks':{k:{'verdict':v['verdict'],'finding_ids':sorted(v['finding_ids'])} for k,v in review['quality_checks'].items()},
            'major_error_ids':sorted(review['major_error_ids']),'major_omission_ids':sorted(review['major_omission_ids']),
            'major_unverified_ids':sorted(review['major_unverified_ids'])}


def review_ok(review):
    return sum(review['scores'].values())>=8 and not any(review[k] for k in ('major_errors','major_omissions','major_unverified'))


class Protocol:
    def __init__(self,root,manifest,helpers):
        self.root=Path(root);self.manifest=manifest;self.r=helpers;self.Error=helpers.EvalError
        self.mapping=self.r.read_json(self.root/'review-map.json')
        self.order=self.r.read_json(self.root/'review-order.json')
        if not isinstance(self.mapping,dict) or not isinstance(self.order,list) or len(self.order)!=len(set(self.order)) or set(self.order)!=set(self.mapping):raise self.Error('匿名映射与顺序不匹配')
        names=[]
        for rid,binding in self.mapping.items():
            if not isinstance(rid,str) or not re.fullmatch('[0-9a-f]{32}',rid) or not isinstance(binding,dict) or set(binding)!={'sample_id','artifact_sha256'} or binding['sample_id'] not in manifest['order'] or not re.fullmatch('[0-9a-f]{64}',binding['artifact_sha256']):raise self.Error('匿名身份或归属无效')
            names.append(binding['sample_id'])
        if len(names)!=len(set(names)):raise self.Error('同一样本存在重复匿名归属')
        self.policy=self.r.read_json(self.root/'harness/issue_policy.json')
        self.rubric={v['id']:v for v in self.r.read_json(self.root/'harness/rubric.json')['rubrics']}
        oracle=self.r.read_json(self.root/'harness/oracle.json')
        self.oracle={v['id']:(s,v) for s in oracle['sources'] for v in s['evidence']}
        if self.policy.get('schema_version')!=1 or set(self.policy.get('special_mappings',{}))!=set(SPECIAL):raise self.Error('冻结问题分类不完整')

    def source(self,ref):
        files={v['path']:v for v in self.manifest['files']}
        if not isinstance(ref,dict) or ref.get('path') not in files:raise self.Error('来源不在冻结清单')
        item=files[ref['path']]
        if ref.get('source_sha256')!=item['sha256']:raise self.Error('来源SHA不匹配')
        a,b=ref.get('start_line'),ref.get('end_line');lines=(self.root/'fixture'/item['path']).read_text().splitlines()
        if type(a) is not int or type(b) is not int or not 1<=a<=b<=len(lines):raise self.Error('来源行范围无效')
        if not isinstance(ref.get('quote'),str) or not ref['quote'].strip() or ref['quote'] not in '\n'.join(lines[a-1:b]):raise self.Error('引文不在真实源区间')
        if set(ref)!={'path','source_sha256','start_line','end_line','quote'}:raise self.Error('来源含未知字段')

    def normalize(self,raw,sample_id=None):
        if not isinstance(raw,dict) or raw.get('schema_version')!=2:raise self.Error('评审须使用schema_version=2')
        identity=raw.get('review_id');binding=self.mapping.get(identity)
        if binding is None or sample_id is not None and binding['sample_id']!=sample_id:raise self.Error('匿名归属不匹配')
        artifact=self.root/'samples'/binding['sample_id']/'workspace'/self.r.OUTPUT
        if artifact.is_symlink() or not artifact.is_file():raise self.Error('缺少可评分普通产物')
        data=artifact.read_bytes();sha=hashlib.sha256(data).hexdigest()
        if raw.get('artifact_sha256')!=sha or binding['artifact_sha256']!=sha:raise self.Error('产物SHA不匹配')
        if not isinstance(raw.get('reviewer'),str) or not raw['reviewer'].strip() or raw.get('method') not in {'human','source_audit_agent'}:raise self.Error('缺少真实同一审查者及方式')
        scores=raw.get('scores',{})
        if set(scores)!=set(SCORES) or any(type(v) is not int or v not in (0,1,2) for v in scores.values()):raise self.Error('五轴必须为0/1/2')
        items=raw.get('findings')
        if not isinstance(items,list) or not items or any(not isinstance(v,dict) for v in items):raise self.Error('缺少真实发现及遗漏')
        findings={v.get('id'):v for v in items}
        if len(findings)!=len(items) or any(not isinstance(k,str) or not k for k in findings):raise self.Error('发现ID无效或重复')
        allowed_types=set(self.policy['issue_types'])
        for f in items:
            types=f.get('issue_types');sev=f.get('severity');kind=f.get('kind')
            if not isinstance(types,list) or not types or len(types)!=len(set(types)) or not set(types)<=allowed_types:raise self.Error('发现须按冻结issue_types归类')
            if sev not in {'none','ordinary','major'}:raise self.Error('发现severity无效')
            if not isinstance(f.get('reason'),str) or not f['reason'].strip():raise self.Error('发现缺少理由')
            if kind=='claim':
                a,b=f.get('start_byte'),f.get('end_byte');text=f.get('claim');verdict=f.get('verdict')
                if type(a) is not int or type(b) is not int or not 0<=a<b<=len(data) or not isinstance(text,str) or not text.strip() or data[a:b]!=text.encode():raise self.Error('claim必须精确绑定真实UTF8输出')
                if verdict not in {'supported','incorrect','unverified'}:raise self.Error('claim verdict无效')
                if not set(f.get('rubric_ids',[]))<=set(self.rubric):raise self.Error('未知rubric')
                if verdict=='supported' and sev!='none' or verdict!='supported' and sev=='none':raise self.Error('判断与severity矛盾')
                if 'omission' in types:raise self.Error('claim不能标omission类别')
            elif kind=='omission':
                if any(k in f for k in ('claim','start_byte','end_byte','verdict')):raise self.Error('遗漏不能伪造输出或claim判断')
                if f.get('rubric_id') not in self.rubric or types!=['omission'] or sev!=self.rubric[f['rubric_id']]['severity_policy']['omitted']:raise self.Error('遗漏须遵守冻结分类和严重度')
            else:raise self.Error('发现kind无效')
            if not isinstance(f.get('evidence'),list) or not f['evidence']:raise self.Error('发现缺少来源依据')
            for ref in f['evidence']:self.source(ref)
        basis=raw.get('score_basis',{})
        if set(basis)!=set(SCORES):raise self.Error('每轴缺少评分依据')
        for v in basis.values():
            if not isinstance(v,dict) or not isinstance(v.get('reason'),str) or not v['reason'].strip() or not isinstance(v.get('finding_ids'),list) or not v['finding_ids'] or not set(v['finding_ids'])<=set(findings):raise self.Error('评分依据未绑定真实发现')
        assessments=raw.get('rubric_assessments',{})
        if set(assessments)!=set(self.rubric):raise self.Error('必须逐项审查全部八项rubric')
        for rid,a in assessments.items():
            status=a.get('status');sev=a.get('severity');ids=a.get('finding_ids')
            if status not in {'supported','incorrect','omitted','unverified'} or sev not in {'none','ordinary','major'} or not isinstance(a.get('reason'),str) or not a['reason'].strip() or not isinstance(ids,list) or not ids or not set(ids)<=set(findings):raise self.Error('逐rubric评审无效')
            if status=='unverified' and self.rubric[rid]['severity_policy']['omitted']=='major' and sev!='major':raise self.Error('核心项待核实不能降普通严重度绕过门槛')
            selected=[findings[k] for k in ids];expected=[self.oracle[k] for k in self.rubric[rid]['evidence_ids']]
            if not any(ref['path']==s['path'] and ref['start_line']<=e['end_line'] and ref['end_line']>=e['start_line'] for f in selected for ref in f['evidence'] for s,e in expected):raise self.Error('rubric未定位到对应来源')
            matched=[f for f in selected if (status=='omitted' and f['kind']=='omission' and f['rubric_id']==rid or status!='omitted' and f['kind']=='claim' and f['verdict']==status and rid in f.get('rubric_ids',[])) and f['severity']==sev]
            if not matched:raise self.Error('rubric与发现判断/严重度不一致')
        incorrect=[f['id'] for f in items if f['kind']=='claim' and f['verdict']=='incorrect'];major_errors=[k for k in incorrect if findings[k]['severity']=='major']
        major_omissions=[f['id'] for f in items if f['kind']=='omission' and f['severity']=='major']
        major_unverified=[f['id'] for f in items if f['kind']=='claim' and f['verdict']=='unverified' and f['severity']=='major']
        if incorrect and scores['correctness']==2 or major_errors and scores['correctness']!=0:raise self.Error('正确性分数与已确认错误矛盾')
        if any(f['kind']=='omission' for f in items) and scores['important_findings']==2:raise self.Error('有明确遗漏时重要发现不能满分')
        quality={}
        for name,categories in self.policy['special_mappings'].items():
            selected=[f for f in items if set(f['issue_types'])&set(categories)]
            status='not_applicable' if not selected else 'incorrect' if any(f['kind']=='omission' or f.get('verdict')=='incorrect' for f in selected) else 'unverified' if any(f.get('verdict')=='unverified' for f in selected) else 'supported'
            quality[name]={'verdict':status,'finding_ids':sorted(f['id'] for f in selected),'reason':'由冻结issue_types映射及逐发现判断派生；普通问题仅按分轴扣分，重大问题阻断质量门槛'}
        result={k:raw[k] for k in ('schema_version','review_id','artifact_sha256','reviewer','method','scores','score_basis','findings','rubric_assessments')}
        derived={'quality_checks':quality,'major_errors':len(major_errors),'major_error_ids':sorted(major_errors),'major_omissions':len(major_omissions),'major_omission_ids':sorted(major_omissions),'major_unverified':len(major_unverified),'major_unverified_ids':sorted(major_unverified)}
        for key,value in derived.items():
            if key in raw and key in {'major_errors','major_omissions','major_unverified'} and type(raw[key]) is not int:raise self.Error('派生计数不得使用bool或未知类型')
            if key in raw and raw[key]!=value:raise self.Error('派生字段不得手填矛盾值：'+key)
        result.update(derived);return result

    def expected_steps(self):
        return [('first',rid) for rid in self.order]+[('reverse',rid) for rid in reversed(self.order)]

    def journal(self):
        path=self.root/'reviews/seals.jsonl';rows=self.r.json_lines(path) if path.exists() else [];prior=''
        for index,row in enumerate(rows):
            if index>=len(self.expected_steps()) or (row.get('phase'),row.get('review_id'))!=self.expected_steps()[index]:raise self.Error('评审封存次序无效')
            value={k:v for k,v in row.items() if k!='event_sha256'}
            if row.get('previous_sha256')!=prior or digest(value)!=row.get('event_sha256'):raise self.Error('评审封存日志损坏')
            path=self.root/'reviews'/row['phase']/(row['review_id']+'.json')
            if not path.is_file() or self.r.digest(path)!=row['snapshot_sha256']:raise self.Error('完整评审封存快照已变化')
            prior=row['event_sha256']
        return rows

    def seal(self,raw,phase):
        if 'reverse_check' in raw:raise self.Error('草稿封存不接受手填一致性结果')
        value=self.normalize(raw);rows=self.journal();key=(phase,value['review_id'])
        if key in [(r['phase'],r['review_id']) for r in rows]:raise self.Error('该阶段已封存，禁止覆盖')
        if len(rows)>=len(self.expected_steps()) or key!=self.expected_steps()[len(rows)]:raise self.Error('必须完成完整初审顺序后才可按反序复查')
        for row in rows:
            old=self.r.read_json(self.root/'reviews'/row['phase']/(row['review_id']+'.json'))
            if old['reviewer']!=value['reviewer'] or old['method']!=value['method']:raise self.Error('同一配对须同审查者和方式')
        path=self.root/'reviews'/phase/(value['review_id']+'.json');path.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
        if path.exists():raise self.Error('评审快照已存在且未提交，禁止猜测恢复覆盖')
        self.r.write_json(path,value)
        row={'sequence':len(rows)+1,'phase':phase,'review_id':value['review_id'],'snapshot_sha256':self.r.digest(path),'semantic_sha256':digest(semantic_projection(value)),'previous_sha256':rows[-1]['event_sha256'] if rows else ''}
        row['event_sha256']=digest(row)
        journal=self.root/'reviews/seals.jsonl'
        with journal.open('a',encoding='utf-8') as stream:stream.write(canonical(row)+'\n');stream.flush();os.fsync(stream.fileno())
        journal.chmod(0o600)
        return {'review_id':value['review_id'],'phase':phase,'snapshot_sha256':row['snapshot_sha256'],'paid_calls':0}

    def reverse(self,rid):
        rows=self.journal()
        if len(rows)!=len(self.expected_steps()):raise self.Error('完整两遍审查尚未封存')
        first=self.r.read_json(self.root/'reviews/first'/(rid+'.json'));second=self.r.read_json(self.root/'reviews/reverse'/(rid+'.json'))
        left,right=semantic_projection(first),semantic_projection(second)
        changes=sorted(k for k in left if left[k]!=right[k])
        return {'first_order':self.order,'reverse_order':list(reversed(self.order)),'consistent':not changes,'initial_scores':first['scores'],
                'first_semantic_sha256':digest(left),'reverse_semantic_sha256':digest(right),'changed_fields':changes,
                'reason':'由完整已封存结构化语义投影计算；忽略理由措辞和展示顺序，来源/归类/判断/评分变化均保留'}

    def final(self,raw,sample_id=None):
        value=self.normalize(raw,sample_id);rid=value['review_id'];reverse=self.reverse(rid)
        sealed=self.r.read_json(self.root/'reviews/reverse'/(rid+'.json'))
        if semantic_projection(value)!=semantic_projection(sealed):raise self.Error('最终导入与反序封存语义不符')
        if 'reverse_check' in raw and raw['reverse_check']!=reverse:raise self.Error('一致性结果由程序派生，禁止手填')
        value=dict(sealed)  # 最终解释也固定取反序快照，不从导入草稿挑选措辞。
        if 'semantic_result' in raw and raw['semantic_result']!=('pass' if review_ok(value) else 'fail'):raise self.Error('最终质量状态与派生门槛不一致')
        value['reverse_check']=reverse
        return value
