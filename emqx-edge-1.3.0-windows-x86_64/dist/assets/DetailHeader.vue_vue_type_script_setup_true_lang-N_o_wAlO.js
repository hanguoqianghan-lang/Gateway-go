import{d as l,u as m,e as d,w as s,r as c,h as o,l as k,f as r,t as b,a as f,o as _,_ as h,i as B,F as x,g,J as y}from"./index-BC0ZidXs.js";import{l as v}from"./lodash-bOCBkqVO.js";import{c as C}from"./createLucideIcon-C8yRCi76.js";/**
 * @license lucide-vue-next v0.546.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const F=C("chevron-left",[["path",{d:"m15 18-6-6 6-6",key:"1wnfg3"}]]),w=l({__name:"BackButton",props:{back:{type:Function}},setup(n){const t=n,{t:a}=m(),e=f(),p=()=>{v.isFunction(t.back)?t.back():e.back()};return(V,D)=>{const u=c("el-icon"),i=c("el-button");return _(),d(i,{type:"primary",class:"back-button",link:"",onClick:p},{default:s(()=>[o(u,{class:"mr-1"},{default:s(()=>[o(r(F))]),_:1}),k(" "+b(r(a)("base.back")),1)]),_:1})}}}),I=h(w,[["__scopeId","data-v-3c298e54"]]),N={class:"text-text-important text-lg font-semibold mb-6"},$=l({__name:"DetailHeader",props:{back:{type:Function}},setup(n){return(t,a)=>{const e=I;return _(),B(x,null,[o(e,{back:t.back,class:"mb-2 -ml-1"},null,8,["back"]),g("div",N,[y(t.$slots,"default")])],64)}}});export{$ as _};
